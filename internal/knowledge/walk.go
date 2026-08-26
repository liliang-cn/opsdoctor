package knowledge

import (
	"context"
	"fmt"
	"sort"
	"strings"

	cortexdb "github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
	"github.com/liliang-cn/cortexdb/v2/pkg/graph"
)

// Walking the graph by name and relation, which is the question retrieval
// cannot answer.
//
// knowledge_search finds text that is ABOUT something and hands back whatever
// happens to be one hop from it. That is the right shape for "why did failover
// stall" and the wrong shape for "which pools back this resource": the second
// has an exact answer sitting in the graph, and asking it through a vector
// index means hoping the right chunk scores well enough to be retrieved and
// that its neighbourhood happens to contain the answer. Measured on the SDS
// base, a `backs` question retrieved three chunks about backing storage in
// general and expanded to twelve neighbours of four types, of which two were
// the answer.
//
// This is also the only thing that reads the link types the ontology registers.
// Before it, `[[relation]] from/to` was a record: the schema carried the ends,
// drift compared against them, and nothing traversed by them. cortexdb decides
// a traversal's direction — and its result type — from the link side, so a walk
// along a declared relation returns only nodes of the far end's declared type.
// That is what the declaration buys at runtime, and it is a different answer
// from an untyped hop: the misdirected edges drift reports are exactly the ones
// a typed walk leaves out.

// WalkStep is one node reached by a walk.
type WalkStep struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Relation reads as a sentence for the same reason a neighbour's does: a
	// direction flag has to be read against a convention stated elsewhere.
	Relation string `json:"relation"`
	Summary  string `json:"summary,omitempty"`
	FilePath string `json:"file,omitempty"`
}

// WalkResult is what a walk found, plus how it was answered.
type WalkResult struct {
	// From is the node the name resolved to, and Match how confidently
	// ("exact", "fold", "contains") — a caller acting on a "contains" hit
	// should be able to decide not to.
	From  string `json:"from"`
	Match string `json:"match,omitempty"`
	// Typed is true when the relation declared its ends, so the walk was
	// constrained to them. False means the ends were open and every edge of
	// that type was followed, whatever it connects.
	Typed bool       `json:"typed"`
	Steps []WalkStep `json:"steps"`
}

// WalkDirection is which way along a relation to travel.
const (
	WalkOut  = "out"  // from the named node outward: "what does it back"
	WalkIn   = "in"   // toward the named node: "what backs it"
	WalkBoth = "both" // either way, direction reported per step
)

// walkLimit bounds one walk. Larger than the retrieval neighbour cap because a
// walk is a precise question — "which pools back this" has a small, complete
// answer, and truncating it silently would make a complete answer look partial.
const walkLimit = 40

// spellingCandidates bounds how many ranked hits a name resolution considers.
// Only those spelling the same name survive poolSpellings, so this is a ceiling
// on the scan rather than on the answer.
const spellingCandidates = 8

// Walk answers "what is <name> related to by <relation>", by traversing the
// graph rather than retrieving text about it.
//
// relation may be empty, in which case every edge type in the domain's
// vocabulary is followed. direction defaults to WalkBoth.
func (s *Store) Walk(ctx context.Context, name, relation, direction string) (*WalkResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("walk needs a starting entity name")
	}
	switch direction {
	case WalkOut, WalkIn, WalkBoth:
	case "":
		direction = WalkBoth
	default:
		return nil, fmt.Errorf("direction must be %q, %q or %q", WalkOut, WalkIn, WalkBoth)
	}

	// More than one candidate, because entity ids keep separators and one name
	// is routinely two nodes — see poolSpellings. Asking for a single best hit
	// made the walk's answer depend on which spelling the caller typed.
	found, err := s.tb.FindNodes(ctx, cortexdb.ToolFindNodesRequest{Names: []string{name}, Limit: spellingCandidates})
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", name, err)
	}
	if len(found.Matches) == 0 || len(found.Matches[0].Nodes) == 0 {
		// Not an error: "the graph does not know this name" is an answer, and
		// one the caller must be able to tell from a failed lookup.
		return &WalkResult{From: name, Steps: []WalkStep{}}, nil
	}
	start := found.Matches[0].Nodes[0]
	res := &WalkResult{From: labelOf(start), Match: found.Matches[0].Match, Steps: []WalkStep{}}
	starts := poolSpellings(found.Matches, name)

	// A relation whose ends the domain declared can be walked by link side,
	// which is what applies the far end's type. One whose ends are open cannot:
	// both sides point at the Entity supertype, nothing is stored under that
	// name, and the traversal would return nothing at all — an empty answer
	// that reads exactly like "this node has no such neighbours".
	if _, typed := s.relationEnds[strings.ToLower(relation)]; typed {
		res.Typed = true
		steps, err := s.walkBySide(ctx, starts, relation, direction)
		if err != nil {
			return nil, err
		}
		res.Steps = steps
		return res, nil
	}

	steps, err := s.walkByEdge(ctx, starts, relation, direction)
	if err != nil {
		return nil, err
	}
	res.Steps = steps
	return res, nil
}

// walkBySide traverses a declared relation through its link sides, so each
// result is a node of the far end's declared type.
//
// The A side carries the relation's own name and runs from → to; the B side is
// named "<relation>_of" and runs the other way. See registerOntology.
func (s *Store) walkBySide(ctx context.Context, startIDs []string, relation, direction string) ([]WalkStep, error) {
	sides := map[string]string{}
	if direction == WalkOut || direction == WalkBoth {
		sides[relation] = WalkOut
	}
	if direction == WalkIn || direction == WalkBoth {
		sides[relation+"_of"] = WalkIn
	}

	// The starting nodes are not their own neighbours: two spellings of one
	// name are often joined to each other, and a walk that returned the other
	// spelling would answer the question with the question.
	seen := make(map[string]struct{}, len(startIDs))
	for _, id := range startIDs {
		seen[id] = struct{}{}
	}
	var steps []WalkStep
	for _, side := range sortedKeys(sides) {
		ids, err := s.db.ResolveObjectSet(ctx, cortexdb.ObjectSet{
			Kind:   cortexdb.ObjectSetSearchAround,
			Source: &cortexdb.ObjectSet{Kind: cortexdb.ObjectSetStatic, ObjectIDs: startIDs},
			Link:   side,
		})
		if err != nil {
			return nil, fmt.Errorf("walk %q: %w", side, err)
		}
		reached := make([]string, 0, len(ids))
		for id := range ids {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			reached = append(reached, id)
		}
		// Sorted so two runs over the same graph answer in the same order; the
		// set comes back as a map.
		sort.Strings(reached)
		nodes, err := s.db.Graph().GetNodesBatch(ctx, reached)
		if err != nil {
			return nil, fmt.Errorf("load walk results: %w", err)
		}
		for _, n := range nodes {
			if n == nil {
				continue
			}
			steps = append(steps, stepFrom(n, relation, sides[side]))
			if len(steps) >= walkLimit {
				return steps, nil
			}
		}
	}
	return steps, nil
}

// walkByEdge is the fallback for a relation with no declared ends, and for a
// walk that names no relation at all.
//
// It follows edges rather than link sides, so nothing constrains what it
// arrives at — which is the honest behaviour for a vocabulary that said nothing
// about the ends. The direction is still reported: that comes from the edge
// itself, not from the ontology.
func (s *Store) walkByEdge(ctx context.Context, startIDs []string, relation, direction string) ([]WalkStep, error) {
	edgeTypes := s.expandEdgeTypes()
	if relation != "" {
		edgeTypes = caseVariants([]string{relation})
	}
	exp, err := s.tb.ExpandGraph(ctx, cortexdb.ToolExpandGraphRequest{
		NodeIDs: startIDs, MaxHops: 1, EdgeTypes: edgeTypes, Limit: walkLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("walk from %s: %w", strings.Join(startIDs, ", "), err)
	}

	seeds := make(map[string]struct{}, len(startIDs))
	for _, id := range startIDs {
		seeds[id] = struct{}{}
	}
	hops := edgesToSeeds(exp.Edges, seeds)
	byID := make(map[string]*graph.GraphNode, len(exp.Nodes))
	for _, n := range exp.Nodes {
		if n != nil {
			byID[n.ID] = n
		}
	}

	ids := make([]string, 0, len(hops))
	for id := range hops {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var steps []WalkStep
	for _, id := range ids {
		h := hops[id]
		if direction == WalkOut && h.direction != DirectionOutgoing {
			continue
		}
		if direction == WalkIn && h.direction != DirectionIncoming {
			continue
		}
		n := byID[id]
		if n == nil {
			continue
		}
		way := WalkOut
		if h.direction == DirectionIncoming {
			way = WalkIn
		}
		steps = append(steps, stepFrom(n, h.edgeType, way))
		if len(steps) >= walkLimit {
			break
		}
	}
	return steps, nil
}

// stepFrom renders one reached node, with the relation as a sentence read from
// the starting node's point of view.
func stepFrom(n *graph.GraphNode, relation, way string) WalkStep {
	label := labelOf(n)
	step := WalkStep{Name: label, Type: n.NodeType}
	if way == WalkIn {
		step.Relation = fmt.Sprintf("%q %s it", label, relation)
	} else {
		step.Relation = fmt.Sprintf("it %s %q", relation, label)
	}
	if p := n.Properties; p != nil {
		if v, ok := p["description"].(string); ok {
			step.Summary = v
		}
		if v, ok := p["file_path"].(string); ok {
			step.FilePath = v
		}
	}
	return step
}

// labelOf prefers the node's name property over its stored content, matching
// how neighbours are labelled elsewhere.
func labelOf(n *graph.GraphNode) string {
	if p := n.Properties; p != nil {
		if v, ok := p["name"].(string); ok && v != "" {
			return v
		}
	}
	if n.Content != "" {
		return n.Content
	}
	return n.ID
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// poolSpellings picks the start nodes that are the queried name, allowing for
// how it was written.
//
// FindNodes ranks and returns every candidate it reached, including ones it got
// to by substring. Taking the best one alone made a walk's answer depend on
// which spelling the caller typed: entity ids keep separators, so "DRBDResource"
// and "DRBD resource" are two nodes, each holding the edges of whichever
// documents used that spelling. Both are the same thing and their neighbours
// belong in one answer.
//
// The rule is separators and case, nothing else. A plural is a different word,
// and folding one would mean guessing at morphology in every language the
// material is written in — so "DRBD resources" is left to doctor to report
// rather than merged here on a hunch. Candidates that survive nothing but a
// substring match are dropped for the same reason.
//
// The best match is always kept, so a name that pools with nothing behaves
// exactly as it did before.
func poolSpellings(matches []cortexdb.ToolNodeNameMatch, name string) []string {
	if len(matches) == 0 || len(matches[0].Nodes) == 0 {
		return nil
	}
	want := spellingKey(name)
	out := []string{matches[0].Nodes[0].ID}
	seen := map[string]struct{}{out[0]: {}}
	for _, m := range matches {
		for _, n := range m.Nodes {
			if n == nil {
				continue
			}
			if _, dup := seen[n.ID]; dup {
				continue
			}
			if spellingKey(labelOf(n)) != want {
				continue
			}
			seen[n.ID] = struct{}{}
			out = append(out, n.ID)
		}
	}
	return out
}

// spellingKey is a name with case and separators removed, which is the
// difference between two spellings of one name and two different names.
//
// A leading dash survives: a flag is not a spelling of an identifier. Without
// it "--read-only" and "ReadOnly" pool, and a question about the API field
// answers with the flag's neighbours. A CLI's help is ingested precisely so an
// agent gets a flag right, and conflating the two spends that. It matches
// cortexdb's canonicalKey, which decides the same thing for a merge.
func spellingKey(name string) string {
	var b strings.Builder
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "-") {
		b.WriteRune('-')
	}
	for _, r := range strings.ToLower(name) {
		switch r {
		case ' ', '_', '-', '.':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
