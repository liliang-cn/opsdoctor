package knowledge

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// Health checks for the retrieval path.
//
// Inventory answers "what is in the store". This answers "does retrieval still
// work", which is a different question and the one that goes wrong silently.
// Every check here exists because the corresponding failure was observed in
// production and produced no error anywhere — the copilot simply answered
// worse, or answered from general knowledge, and nothing said why:
//
//   - A system-wide proxy env var routed the embedder through a host that no
//     longer existed. Chat kept working (its endpoint was in NO_PROXY), so the
//     assistant looked healthy while every knowledge_search failed.
//   - The vector index returned ~50 rows however large a TopK was asked for,
//     because the HNSW ef parameter bounded the candidate list below k. Deep
//     retrieval was impossible and nothing reported a limit.
//   - One imported code graph outnumbered the prose ten to one, so operational
//     questions retrieved code. The answers stayed fluent; only their sources
//     changed.

// CheckStatus is the verdict for one check.
type CheckStatus string

const (
	CheckOK   CheckStatus = "ok"
	CheckWarn CheckStatus = "warn"
	CheckFail CheckStatus = "fail"
)

// Check is one diagnostic result.
type Check struct {
	Name   string
	Status CheckStatus
	Detail string
	// Hint is what to do about it, present only when something is wrong.
	Hint string
}

// Diagnosis is the full report. Failed is true when any check failed, which is
// what a caller should use for an exit code.
type Diagnosis struct {
	Checks []Check
	Failed bool
}

func (d *Diagnosis) add(c Check) {
	if c.Status == CheckFail {
		d.Failed = true
	}
	d.Checks = append(d.Checks, c)
}

// Doctor runs every check against this store and its configured embedder.
func (s *Store) Doctor(ctx context.Context, embBaseURL string) *Diagnosis {
	d := &Diagnosis{}
	d.add(checkProxy(embBaseURL))
	d.add(s.checkEmbedder(ctx))
	inv, invErr := s.Inventory(ctx)
	d.add(checkInventory(inv, invErr))
	d.add(s.checkRecallDepth(ctx))
	d.add(s.checkSourceBalance(ctx, inv, invErr))
	d.add(s.checkOrphanNodes(ctx))
	d.add(checkVocabularyDrift(inv, invErr))
	d.add(s.checkDuplicateEntities(ctx))
	return d
}

// checkVocabularyDrift reports where the graph left the vocabulary the domain
// declared.
//
// DriftReport has computed this since the ontology was first registered, and
// Inventory has carried it — but nothing printed it, so the answer existed and
// no operator could reach it without writing Go. That is the same shape as the
// failure it was written to catch: a live SDS base carrying a "StorageClass"
// node type nobody declared, invisible until someone ran a GROUP BY by hand.
func checkVocabularyDrift(inv *Inventory, invErr error) Check {
	const name = "vocabulary drift"
	if invErr != nil || inv == nil {
		return Check{Name: name, Status: CheckWarn, Detail: "inventory unavailable"}
	}
	d := inv.Drift
	if d == nil || !d.Registered {
		return Check{Name: name, Status: CheckOK,
			Detail: "no prose vocabulary declared; expansion uses the built-in code edges"}
	}

	// A graph extracted under one vocabulary and queried under another is the
	// expensive case and the quiet one: the edges are all there, and expansion
	// filters every one of them out.
	if d.StoredFingerprint != "" && d.StoredFingerprint != d.Fingerprint {
		return Check{Name: name, Status: CheckWarn,
			Detail: fmt.Sprintf("graph was extracted under vocabulary %s, domain.toml now declares %s",
				d.StoredFingerprint, d.Fingerprint),
			Hint: "edges spelled in the old vocabulary are stored and never traversed, so GraphRAG " +
				"quietly degrades to plain vector search. Re-ingest the affected sources, or restore " +
				"the previous vocabulary.",
		}
	}

	// A relation pointing the wrong way outranks an invented type. An invented
	// type is inert — stored, not walked, visible as an absence. A backwards
	// edge IS walked, and produces an answer that states the opposite of what
	// the source said, fluently.
	if len(d.MisdirectedEdges) > 0 {
		m := d.MisdirectedEdges[0]
		// The count and the shape have to be about the same edges. Pairing the
		// relation's total with its commonest shape read as a claim about every
		// one of them: "20 edges are ConfigParameter → ConfigParameter" when
		// twelve were, and the other eight were four other shapes. Someone who
		// goes looking for twenty finds twelve and stops trusting the number.
		detail := fmt.Sprintf("%s: %d edges are %s, declared %s", m.Relation, m.Count, m.Got, m.Want)
		if m.GotCount > 0 && m.GotCount < m.Count {
			detail = fmt.Sprintf("%s: %d edges do not match the declared %s; the commonest is %d × %s",
				m.Relation, m.Count, m.Want, m.GotCount, m.Got)
		}
		if n := len(d.MisdirectedEdges); n > 1 {
			detail += fmt.Sprintf(" (+%d more relations)", n-1)
		}
		hint := "an edge stored the wrong way round is still traversed, so the answer it supports " +
			"states the opposite of the source and reads exactly as well."
		if m.Reversed == m.Count {
			hint += fmt.Sprintf(" Every %q edge is exactly reversed, which is one wrong line in "+
				"domain.toml rather than the model erring — check the from/to on that [[relation]].", m.Relation)
		} else {
			hint += " Re-ingest the sources these came from; if it persists, the extraction prompt is " +
				"ambiguous about the direction."
		}
		// Where to look, rather than what to do: these edges cluster on a few
		// nodes, and the two that carried six of eighteen on a live base needed
		// opposite fixes — one was a mistyped node, the other a misused
		// relation. Naming them is a fact; proposing the retype would have been
		// wrong half the time.
		if names, carried := misdirectedNodeSummary(d.MisdirectedNodes); names != "" {
			hint += fmt.Sprintf(" Most of them run through few nodes: %s — %d of %d edges.",
				names, carried, totalMisdirected(d.MisdirectedEdges))
		}
		return Check{Name: name, Status: CheckWarn, Detail: detail, Hint: hint}
	}

	if !d.Clean() {
		var parts []string
		if n := len(d.UndeclaredEdgeTypes); n > 0 {
			parts = append(parts, fmt.Sprintf("%d undeclared edge types (%s)", n, topTypes(d.UndeclaredEdgeTypes)))
		}
		if n := len(d.UndeclaredNodeTypes); n > 0 {
			parts = append(parts, fmt.Sprintf("%d undeclared node types (%s)", n, topTypes(d.UndeclaredNodeTypes)))
		}
		return Check{Name: name, Status: CheckWarn, Detail: strings.Join(parts, ", "),
			Hint: "the extracting model invented these. An undeclared EDGE type is the costly one — " +
				"expansion filters on the declared vocabulary, so those edges are stored and never " +
				"walked. Either add the type to domain.toml or re-ingest so it stops being emitted.",
		}
	}

	detail := "graph stays inside the declared vocabulary"
	if n := len(d.UnusedNodeTypes) + len(d.UnusedEdgeTypes); n > 0 {
		detail += fmt.Sprintf("; %d declared types never extracted", n)
	}
	return Check{Name: name, Status: CheckOK, Detail: detail}
}

// topTypes names the worst offenders, most rows first, so a detail line stays
// one line however wide the drift is.
func topTypes(counts map[string]int) string {
	const show = 3
	types := make([]string, 0, len(counts))
	for t := range counts {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool {
		if counts[types[i]] != counts[types[j]] {
			return counts[types[i]] > counts[types[j]]
		}
		return types[i] < types[j]
	})
	var parts []string
	for i, t := range types {
		if i == show {
			parts = append(parts, fmt.Sprintf("+%d more", len(types)-show))
			break
		}
		parts = append(parts, fmt.Sprintf("%s×%d", t, counts[t]))
	}
	return strings.Join(parts, ", ")
}

// checkProxy reports when the embedder endpoint would be sent through an HTTP
// proxy. Being proxied is legitimate; being proxied through a host that does not
// answer is the failure, and it presents as every search erroring at once.
func checkProxy(embBaseURL string) Check {
	const name = "embedder proxy"
	if strings.TrimSpace(embBaseURL) == "" {
		return Check{Name: name, Status: CheckOK, Detail: "no embedder endpoint configured"}
	}
	u, err := url.Parse(embBaseURL)
	if err != nil {
		return Check{Name: name, Status: CheckWarn, Detail: fmt.Sprintf("cannot parse %q", embBaseURL)}
	}
	proxyURL, err := http.ProxyFromEnvironment(&http.Request{URL: u})
	if err != nil {
		return Check{Name: name, Status: CheckWarn, Detail: fmt.Sprintf("proxy lookup failed: %v", err)}
	}
	if proxyURL == nil {
		return Check{Name: name, Status: CheckOK, Detail: "direct (not proxied)"}
	}
	// Reachability is proved by the embedder check below; this one's job is to
	// make the proxy visible, because an unreachable one is invisible otherwise.
	return Check{
		Name:   name,
		Status: CheckWarn,
		Detail: fmt.Sprintf("%s is routed through %s", u.Host, proxyURL.Host),
		Hint: "if embedding calls fail, this proxy is the first suspect — a stale HTTP_PROXY " +
			"takes the whole knowledge base down while chat keeps working. Add the endpoint to " +
			"NO_PROXY (a systemd unit's Environment= overrides its EnvironmentFile=, so it must " +
			"go in a drop-in).",
	}
}

// checkEmbedder embeds one short string. This is the end-to-end proof that the
// endpoint, the credentials and the network path all work.
func (s *Store) checkEmbedder(ctx context.Context) Check {
	const name = "embedder reachable"
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	start := time.Now()
	_, err := s.db.HybridSearchText(ctx, "healthcheck", 1)
	if err != nil {
		return Check{
			Name: name, Status: CheckFail,
			Detail: err.Error(),
			Hint: "every knowledge_search fails this way, and the agent will answer from general " +
				"knowledge instead of saying so. Check OSS_EMB_BASE_URL/OSS_EMB_API_KEY and the proxy above.",
		}
	}
	return Check{Name: name, Status: CheckOK, Detail: fmt.Sprintf("responded in %s", time.Since(start).Round(time.Millisecond))}
}

func checkInventory(inv *Inventory, err error) Check {
	const name = "store contents"
	if err != nil {
		return Check{Name: name, Status: CheckFail, Detail: err.Error()}
	}
	if inv.Chunks == 0 {
		return Check{
			Name: name, Status: CheckFail, Detail: "no chunks stored",
			Hint: "nothing has been ingested — run `oss-agent ingest <dir>` or `ingest-repo <url>`.",
		}
	}
	return Check{
		Name: name, Status: CheckOK,
		Detail: fmt.Sprintf("%d sources, %d chunks, %d nodes, %d edges, dim %d",
			len(inv.Sources), inv.Chunks, inv.Nodes, inv.Edges, inv.Dim),
	}
}

// recallProbeK is deliberately well past any index's default candidate list.
// The bug this catches returned exactly the index's ef, whatever was asked for.
const recallProbeK = 200

// checkRecallDepth asks for far more candidates than a normal search and reports
// how many come back.
//
// A vector index that caps its result set silently makes deep retrieval
// impossible: over-fetching callers, quota schemes that promote from a tail, and
// anything paginating all quietly see the same ceiling. The symptom is a round
// number of results that does not change when the request grows.
func (s *Store) checkRecallDepth(ctx context.Context) Check {
	const name = "recall depth"
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	small, err := s.db.HybridSearchText(ctx, "the", 10)
	if err != nil {
		return Check{Name: name, Status: CheckWarn, Detail: "probe failed: " + err.Error()}
	}
	large, err := s.db.HybridSearchText(ctx, "the", recallProbeK)
	if err != nil {
		return Check{Name: name, Status: CheckWarn, Detail: "probe failed: " + err.Error()}
	}
	detail := fmt.Sprintf("asked for %d, got %d (asked for 10, got %d)", recallProbeK, len(large), len(small))

	// Fewer rows than asked for is normal on a small corpus; the tell is a
	// result set that stops growing at a suspiciously round number while the
	// store clearly holds more.
	if len(large) == len(small) || len(large) >= recallProbeK {
		return Check{Name: name, Status: CheckOK, Detail: detail}
	}
	if inv, err := s.Inventory(ctx); err == nil && inv.Chunks > len(large)*2 && len(large)%10 == 0 {
		return Check{
			Name: name, Status: CheckWarn,
			Detail: detail + fmt.Sprintf("; the store holds %d chunks", inv.Chunks),
			Hint: "a recall that stops at a round number well below the corpus size usually means the " +
				"vector index is bounded below the requested k (HNSW ef, IVF nprobe). Deep retrieval " +
				"and any over-fetching caller silently hit that ceiling.",
		}
	}
	return Check{Name: name, Status: CheckOK, Detail: detail}
}

// checkSourceBalance reports how the corpus splits between imported code and
// prose, because one code graph can outnumber the runbooks by an order of
// magnitude and then answer operational questions.
func (s *Store) checkSourceBalance(ctx context.Context, inv *Inventory, invErr error) Check {
	const name = "source balance"
	if invErr != nil || inv == nil {
		return Check{Name: name, Status: CheckWarn, Detail: "inventory unavailable"}
	}
	var code, prose int
	for _, src := range inv.Sources {
		if isCodeSource(nil, src.DocumentID) {
			code += src.Chunks
		} else {
			prose += src.Chunks
		}
	}
	if code == 0 {
		return Check{Name: name, Status: CheckOK, Detail: fmt.Sprintf("%d prose chunks, no imported code graph", prose)}
	}
	detail := fmt.Sprintf("%d code chunks vs %d prose chunks", code, prose)
	if prose == 0 {
		return Check{Name: name, Status: CheckWarn, Detail: detail,
			Hint: "a code-only base cannot answer an operational question; ingest the runbooks too."}
	}
	// The quota bounds code at half of any result set, so imbalance is handled
	// at retrieval time. It is still worth seeing: the wider the gap, the more
	// the answer depends on that quota holding.
	if ratio := float64(code) / float64(prose); ratio > 5 {
		return Check{Name: name, Status: CheckWarn,
			Detail: detail + fmt.Sprintf(" (%.0fx)", ratio),
			Hint: "retrieval caps the code share per result set, so this is not fatal, but verify " +
				"operational questions still return runbooks — `oss-agent eval` reports the mix per question.",
		}
	}
	return Check{Name: name, Status: CheckOK, Detail: detail}
}

// checkOrphanNodes counts graph nodes with no edges. A large share means writes
// landed but their edges did not — the state a store reaches when mention edges
// are rejected, or when repeated ingests accumulate entities nothing can reach.
func (s *Store) checkOrphanNodes(ctx context.Context) Check {
	const name = "graph connectivity"
	var total, orphans int
	row := s.db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM graph_nodes`)
	if err := row.Scan(&total); err != nil {
		return Check{Name: name, Status: CheckWarn, Detail: err.Error()}
	}
	if total == 0 {
		return Check{Name: name, Status: CheckOK, Detail: "no graph nodes"}
	}
	row = s.db.SQL().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM graph_nodes n
		WHERE NOT EXISTS (SELECT 1 FROM graph_edges e WHERE e.from_node_id = n.id OR e.to_node_id = n.id)`)
	if err := row.Scan(&orphans); err != nil {
		return Check{Name: name, Status: CheckWarn, Detail: err.Error()}
	}
	share := float64(orphans) / float64(total)
	detail := fmt.Sprintf("%d of %d nodes have no edges (%.0f%%)", orphans, total, share*100)
	if share > 0.15 {
		return Check{Name: name, Status: CheckWarn, Detail: detail,
			Hint: "unreachable nodes are retrieved but never expanded. They accumulate when an " +
				"ingest's edges are rejected, or when a source is re-ingested without being purged " +
				"first — `oss-agent refresh <dir>` purges before re-ingesting.",
		}
	}
	return Check{Name: name, Status: CheckOK, Detail: detail}
}

// Format renders a report for a terminal.
func (d *Diagnosis) Format() string {
	var b strings.Builder
	for _, c := range d.Checks {
		mark := "ok  "
		switch c.Status {
		case CheckWarn:
			mark = "warn"
		case CheckFail:
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "[%s] %-22s %s\n", mark, c.Name, c.Detail)
		if c.Hint != "" {
			for _, line := range wrapHint(c.Hint, 76) {
				fmt.Fprintf(&b, "       %s\n", line)
			}
		}
	}
	return b.String()
}

func wrapHint(s string, width int) []string {
	words := strings.Fields(s)
	var lines []string
	cur := ""
	for _, w := range words {
		if cur == "" {
			cur = w
			continue
		}
		if len(cur)+1+len(w) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur += " " + w
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// ProxyEnvSummary lists the proxy variables in effect, for a caller that wants
// to print them alongside a failure.
func ProxyEnvSummary() string {
	var set []string
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
		if v := os.Getenv(k); v != "" {
			set = append(set, k+"="+v)
		}
	}
	sort.Strings(set)
	return strings.Join(set, "\n")
}

// checkDuplicateEntities reports one concept stored under two spellings.
//
// Entity ids are derived from the name with separators kept, so "DRBDResource"
// lands on entity:drbdresource and "DRBD resource" on entity:drbd_resource.
// Nothing else notices: both carry the declared type, so drift is satisfied;
// both have edges, so connectivity is satisfied; and the graph simply reports
// two entities where the material describes one. What splits is the edges, each
// document attaching to whichever spelling it happened to use.
//
// A walk pools the spellings at query time, so this is no longer wrong answers
// — but the pair still means the extractor was inconsistent about a name it
// uses often, and re-ingesting those sources is what actually fixes it.
//
// Only nodes typed by the prose vocabulary are compared. The code graph names
// one symbol per package, so main.go in seven packages is seven files rather
// than seven spellings of one.
func (s *Store) checkDuplicateEntities(ctx context.Context) Check {
	const name = "entity spellings"
	if len(s.entityTypes) == 0 {
		return Check{Name: name, Status: CheckOK, Detail: "no prose vocabulary declared"}
	}

	rows, err := s.db.SQL().QueryContext(ctx,
		`SELECT content, node_type FROM graph_nodes WHERE id LIKE 'entity:%' AND content <> ''`)
	if err != nil {
		return Check{Name: name, Status: CheckWarn, Detail: err.Error()}
	}
	defer func() { _ = rows.Close() }()

	groups := make(map[string]map[string]struct{})
	for rows.Next() {
		var content, nodeType string
		if err := rows.Scan(&content, &nodeType); err != nil {
			return Check{Name: name, Status: CheckWarn, Detail: err.Error()}
		}
		if !s.declaresNodeType(nodeType) {
			continue
		}
		key := spellingKey(content)
		if key == "" {
			continue
		}
		if groups[key] == nil {
			groups[key] = make(map[string]struct{})
		}
		groups[key][content] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return Check{Name: name, Status: CheckWarn, Detail: err.Error()}
	}

	var examples []string
	pairs := 0
	for _, spellings := range groups {
		if len(spellings) < 2 {
			continue
		}
		pairs++
		if len(examples) < 3 {
			examples = append(examples, strings.Join(sortedSet(spellings), " / "))
		}
	}
	if pairs == 0 {
		return Check{Name: name, Status: CheckOK, Detail: "every entity is spelled one way"}
	}

	sort.Strings(examples)
	detail := fmt.Sprintf("%d concepts are stored under more than one spelling: %s",
		pairs, strings.Join(examples, "; "))
	if pairs > len(examples) {
		detail += fmt.Sprintf(" (+%d more)", pairs-len(examples))
	}
	return Check{Name: name, Status: CheckWarn, Detail: detail,
		Hint: "each spelling is its own node, so a document's edges attach to whichever one it " +
			"used. Walks pool them, but expansion and the neighbour quota still see two entities. " +
			"Run `oss-agent resolve --dry-run` to see the merges, then without the flag to make them."}
}

// sortedSet renders a set of spellings in a stable order.
func sortedSet(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// misdirectedNodeSummary renders the nodes carrying the most misdirected edges,
// and how many they carry between them. Empty when there is no concentration to
// point at — a list of nodes with one edge each is the edge list again.
func misdirectedNodeSummary(nodes []MisdirectedNode) (string, int) {
	var parts []string
	carried := 0
	for _, n := range nodes {
		if n.Edges < 2 || len(parts) == 3 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%q (a %s, %d)", n.Name, n.Type, n.Edges))
		carried += n.Edges
	}
	if len(parts) == 0 {
		return "", 0
	}
	return strings.Join(parts, ", "), carried
}

// totalMisdirected sums every relation's wrong edges, which is what the node
// counts should be read against.
func totalMisdirected(edges []MisdirectedEdge) int {
	total := 0
	for _, e := range edges {
		total += e.Count
	}
	return total
}
