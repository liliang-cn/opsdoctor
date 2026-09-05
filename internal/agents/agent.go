// Package agents builds the ops agent: an agent-go service wired with a frontier
// LLM, a cortexdb-backed graph memory, the domain's read-only probe tools, a
// GraphRAG knowledge_search tool, and the deterministic red-line safety wall.
// Everything product-specific comes from the *domain.Domain, so the same builder
// serves any storage or infrastructure product.
package agents

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	agdomain "github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/providers"

	"github.com/liliang-cn/opsdoctor/internal/cite"
	"github.com/liliang-cn/opsdoctor/internal/config"
	"github.com/liliang-cn/opsdoctor/internal/domain"
	"github.com/liliang-cn/opsdoctor/internal/extract"
	"github.com/liliang-cn/opsdoctor/internal/graphimport"
	"github.com/liliang-cn/opsdoctor/internal/knowledge"
	"github.com/liliang-cn/opsdoctor/internal/probes"
	"github.com/liliang-cn/opsdoctor/internal/safety"
	"github.com/liliang-cn/opsdoctor/internal/schemaimport"
)

// LLM builds a bare LLM generator from config (used by scaffolding/extraction
// paths that need raw generation without the full agent service).
func LLM(cfg config.Config) (agdomain.Generator, error) {
	return providers.NewOpenAILLMProvider(&agdomain.OpenAIProviderConfig{
		BaseURL:  cfg.LLMBaseURL,
		APIKey:   cfg.LLMAPIKey,
		LLMModel: cfg.LLMModel,
	})
}

// StoreOptions is what a domain's vocabulary means to a knowledge store, in one
// place, for every caller that opens one.
//
// It was previously written out at each call site, and most call sites did not
// write it: eleven of thirteen CLI commands opened the store with no vocabulary
// at all, so `doctor` measured drift against nothing and reported a
// twenty-seven-type domain as declaring none. Nothing failed — the vocabulary
// was simply absent, per command, and which commands had it was something you
// learned by reading each one.
//
// The relation half is what retrieval-time graph expansion traverses: the edges
// in the graph are the ones extraction was told to produce, so the whitelist has
// to come from the same declaration. The entity half is registered as a cortexdb
// ontology schema, which records the vocabulary the graph was extracted under,
// gives drift a baseline, and is what seeding asks for by interface.
func StoreOptions(dom *domain.Domain) []knowledge.Option {
	ends := make([]knowledge.RelationEnd, 0, len(dom.Relations))
	for _, r := range dom.Relations {
		ends = append(ends, knowledge.RelationEnd{Name: r.Name, From: r.From, To: r.To})
	}
	return []knowledge.Option{
		knowledge.WithRelationTypes(dom.RelationTypes),
		knowledge.WithOntology(dom.Name, dom.EntityTypes, dom.RelationTypes),
		knowledge.WithRelationEnds(ends),
		knowledge.WithImportedVocabulary(importedEntityTypes(dom), importedRelationTypes(dom)),
	}
}

// importedEntityTypes and importedRelationTypes are everything an importer may
// write: the domain's code vocabulary, which admits the graph file, plus the
// structural types the importers add on their own. Assembled here rather than
// in the store because the store cannot know the importers — they import it.
func importedEntityTypes(dom *domain.Domain) []string {
	out := append([]string(nil), dom.Vocabulary.Code.EntityTypes...)
	out = append(out, graphimport.StructuralEntityTypes...)
	return append(out, schemaimport.TableType)
}

func importedRelationTypes(dom *domain.Domain) []string {
	out := append([]string(nil), dom.Vocabulary.Code.RelationTypes...)
	out = append(out, graphimport.StructuralRelationTypes...)
	return append(out, schemaimport.ReferencesType)
}

// BuildExtractor returns an LLM ontology extractor for the domain, or nil if no
// LLM key is configured (ingestion then stores vectors without graph extraction).
func BuildExtractor(cfg config.Config, dom *domain.Domain) *extract.Extractor {
	if cfg.LLMAPIKey == "" {
		return nil
	}
	llm, err := providers.NewOpenAILLMProvider(&agdomain.OpenAIProviderConfig{
		BaseURL:  cfg.LLMBaseURL,
		APIKey:   cfg.LLMAPIKey,
		LLMModel: cfg.LLMModel,
	})
	if err != nil {
		return nil
	}
	return extract.New(llm, dom)
}

// Build constructs the configured agent service and its knowledge store for the
// given domain. Caller must Close both.
func Build(cfg config.Config, dom *domain.Domain) (*agent.Service, *knowledge.Store, error) {
	llm, err := providers.NewOpenAILLMProvider(&agdomain.OpenAIProviderConfig{
		BaseURL:  cfg.LLMBaseURL,
		APIKey:   cfg.LLMAPIKey,
		LLMModel: cfg.LLMModel,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("init llm: %w", err)
	}
	emb, err := providers.NewOpenAIEmbedderProvider(&agdomain.OpenAIProviderConfig{
		BaseURL:        cfg.EmbBaseURL,
		APIKey:         cfg.EmbAPIKey,
		EmbeddingModel: cfg.EmbModel,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("init embedder: %w", err)
	}

	store, err := knowledge.Open(cfg.KnowledgeDBPath, cfg.EmbBaseURL, cfg.EmbAPIKey, cfg.EmbModel, cfg.EmbDim,
		StoreOptions(dom)...)
	if err != nil {
		return nil, nil, fmt.Errorf("open knowledge: %w", err)
	}

	// Note: the knowledge base is our own cortexdb (knowledge_search tool), so we
	// don't enable agent-go's separate graph memory here. The embedder is wired in
	// case skills/RAG use it.
	//
	// There is no PTC switch to set any more. agent-go v2 had Programmatic Tool
	// Calling — the model emitted code that called tools, then split its reply
	// between a "Return Value" summary and "Logs", which Text() concatenated into
	// a noisy dump — and this agent explicitly turned it off. v3 removed the
	// feature outright, so plain function-calling is now the only mode: still
	// iterative ReAct tool use, still a single clean text answer in FinalResult,
	// which is the shape an ask/diagnose agent wants.
	svc, err := agent.New("opsdoctor").
		WithSystemPrompt(dom.Persona + groundingDirective + citationDirective).
		WithLLM(llm).
		WithEmbedder(emb).
		WithSkills(). // loads ~/.agentgo/skills (understand-* codebase-comprehension skills)
		WithDBPath(cfg.DBPath).
		Build()
	if err != nil {
		store.Close()
		return nil, nil, fmt.Errorf("build agent: %w", err)
	}

	filter, err := safety.NewFromSpecs(dom.RedLines)
	if err != nil {
		svc.Close()
		store.Close()
		return nil, nil, fmt.Errorf("compile red-lines: %w", err)
	}
	registerProbes(svc, dom.Probes, filter)
	registerKnowledgeSearch(svc, store)
	registerGraphWalk(svc, store, dom.RelationTypes)
	registerSafetyLint(svc, filter)
	registerSuggestAction(svc, filter)
	return svc, store, nil
}

// SuggestActionToolName is the tool the agent calls to propose a state-changing
// action for operator approval (it never executes anything). Stream converts the
// tool's result into an EventSuggestion.
const SuggestActionToolName = "suggest_action"

// registerSuggestAction exposes the structured suggestion channel. The handler
// does NOT execute anything: it runs the proposed action through the deterministic
// red-line wall, then returns the structured proposal (action, params, reason,
// severity, verdict) so the streaming layer can render an approve card while the
// ReAct loop continues from a short confirmation message.
func registerSuggestAction(svc *agent.Service, filter *safety.Filter) {
	params := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Machine-readable action id to propose, e.g. \"ha.evict\" or \"resource.promote\". Never executed — only proposed for operator approval.",
			},
			"params": map[string]interface{}{
				"type":        "object",
				"description": "Arbitrary key/value parameters for the action (e.g. node, resource, volume).",
			},
			"reason": map[string]interface{}{
				"type":        "string",
				"description": "Why this action is recommended, grounded in the current symptom/evidence.",
			},
			"severity": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"low", "medium", "high"},
				"description": "Operator-facing risk level. Defaults to \"medium\".",
			},
		},
		"required": []string{"action", "reason"},
	}
	svc.AddTool(SuggestActionToolName,
		"Propose a state-changing action for a human operator to approve. This does NOT execute "+
			"anything — use it whenever the safe next step is a mutation (evict, promote, resize, "+
			"restart, …) instead of describing the raw command. The proposal is checked against the "+
			"red-line wall and surfaced to the operator as an approve/reject card.",
		params,
		func(_ context.Context, args map[string]interface{}) (interface{}, error) {
			return suggestionResult(filter, args), nil
		})
}

// suggestionResult is the pure core of the suggest_action handler: it normalizes
// the proposal, runs it through the red-line wall, and returns the JSON-safe result
// the streaming layer converts into an EventSuggestion. It executes nothing.
func suggestionResult(filter *safety.Filter, args map[string]interface{}) map[string]interface{} {
	action, _ := args["action"].(string)
	reason, _ := args["reason"].(string)
	severity, _ := args["severity"].(string)
	if severity == "" {
		severity = "medium"
	}
	var p map[string]interface{}
	if raw, ok := args["params"].(map[string]interface{}); ok {
		p = raw
	}
	verdict := filter.Check(actionCommandString(action, p))
	return map[string]interface{}{
		"ok":      true,
		"message": "suggestion recorded; awaiting operator approval",
		"suggestion": map[string]interface{}{
			"action":   action,
			"params":   p,
			"reason":   reason,
			"severity": severity,
			"verdict":  verdict,
		},
	}
}

// actionCommandString renders a proposal as a single command-like line so the
// deterministic red-line wall (which matches shell command patterns) can rule on
// it: "action k1=v1 k2=v2" with keys sorted for stable matching.
func actionCommandString(action string, params map[string]interface{}) string {
	if len(params) == 0 {
		return action
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(action)
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%v", k, params[k])
	}
	return b.String()
}

// citationDirective is appended to every domain's persona so answers cite their
// retrieved evidence regardless of what the domain.toml persona says. Each
// knowledge_search hit carries a stable, readable "cite" label derived from its
// source, so the same source keeps the same label across every search in a turn.
const citationDirective = `

Citations (required — do this every time you use knowledge_search):
- Each hit has a "cite" label (e.g. drbd-troubleshooting.adoc) and a full "source".
  When a statement in your answer comes from a hit, append its label in square
  brackets right after the claim, e.g. "... triggers a full resync [drbd-troubleshooting].".
- The same source always has the same label — reuse it, and combine like [a][b].
- End the answer with a "Sources" section: one line per label you cited,
  "- [label] full-source". List each source once; only labels that appeared in
  knowledge_search results — never invent one.`

// groundingDirective is appended to every persona, like citationDirective and
// for the same reason: these two rules are not a property of any one product,
// and a domain.toml that forgets them produces an agent that is confidently
// wrong rather than merely unhelpful.
//
// Both were written from observed failures on a deployed copilot:
//
//   - It searched only for the topics its persona happened to enumerate. Asked
//     whether adding a second remote channel to a specific host would help, it
//     searched nothing and recommended the tunnel the knowledge base records as
//     having already failed there — the host's NIC hangs and takes every channel
//     over it down, which is exactly the sort of thing a knowledge base holds
//     and general knowledge does not.
//   - Asked for the function behind a feature, it invented a plausible name and
//     file path, then a whole call chain around them. Nothing marked it as a
//     guess, and the paths do not exist.
const groundingDirective = `

Grounding (required):
- Search the knowledge base FIRST, on essentially every question, before
  answering from what you already know. It is not a reference manual; it is this
  deployment's own memory: notes about named hosts, what was tried and failed,
  why a workaround was abandoned. Such notes routinely contradict generic best
  practice, and a generically correct answer given while one exists is worse than
  no answer — it sends an operator to repeat a known failure. A question naming a
  host, a resource, a symptom or a past event is a search, not a recall.
- NEVER invent an identifier. Function names, file paths, call chains, CLI flags,
  config keys, API fields: state them only if they appeared in something you
  retrieved this turn. You cannot read the working tree, so an identifier you did
  not retrieve is a guess, and a guess formatted as a code reference is
  indistinguishable from fact until it fails in the operator's hands. When a
  search comes back empty, say what you searched for and stop.`

// registerGraphWalk exposes traversal by name and relation.
//
// knowledge_search finds text that is ABOUT something and returns whatever is
// one hop from it. That is right for "why did failover stall" and wrong for
// "which pools back this resource": the second has an exact answer sitting in
// the graph, and routing it through a vector index means hoping the right chunk
// scores well AND that its neighbourhood happens to contain the answer. Two
// coin flips for a question the graph can answer outright.
//
// It is registered only when the domain declares a relation vocabulary. With no
// relations to name, the tool degrades to "give me any neighbour", which
// knowledge_search already does better because it also brings the text.
func registerGraphWalk(svc *agent.Service, store *knowledge.Store, relations []string) {
	if len(relations) == 0 {
		return
	}
	params := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"entity": map[string]interface{}{
				"type":        "string",
				"description": "The name of the thing to start from, as it appears in the material (a resource, pool, node, gateway, command).",
			},
			"relation": map[string]interface{}{
				"type": "string",
				"description": "Which relation to follow. Omit to follow every relation. One of: " +
					strings.Join(relations, ", ") + ".",
			},
			"direction": map[string]interface{}{
				"type": "string",
				"enum": []string{knowledge.WalkOut, knowledge.WalkIn, knowledge.WalkBoth},
				"description": "out = what the entity does to others (\"what does it back\"); " +
					"in = what others do to it (\"what backs it\"); both = either. Defaults to both.",
			},
		},
		"required": []string{"entity"},
	}
	svc.AddTool("graph_walk",
		"Follow the knowledge graph from a named thing along a relation, in a stated direction. "+
			"Use this when the question is about how two kinds of thing are connected and you can name "+
			"the starting one — \"which pools back r0\", \"what does this promoter config promote\", "+
			"\"what state is this resource in\". It answers from the graph's edges, not from retrieved "+
			"text, so it neither invents nor omits. Use knowledge_search instead when you need the prose: "+
			"procedures, causes, incident history.",
		params,
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			entity, _ := args["entity"].(string)
			relation, _ := args["relation"].(string)
			direction, _ := args["direction"].(string)

			res, err := store.Walk(ctx, entity, relation, direction)
			if err != nil {
				return map[string]interface{}{"ok": false, "error": err.Error()}, nil
			}
			out := map[string]interface{}{
				"ok":    true,
				"from":  res.From,
				"steps": res.Steps,
			}
			if len(res.Steps) == 0 {
				// An empty walk and a walk that found nothing are the same
				// shape, and the difference matters: the graph genuinely not
				// connecting two things is a finding, while a name it never
				// heard of is a dead end to back out of rather than report.
				if res.Match == "" {
					out["instruction"] = "The graph holds nothing by that name. Check the spelling against " +
						"something you retrieved, or use knowledge_search to find what it is called."
				} else {
					out["instruction"] = "That entity exists in the graph but nothing is connected to it " +
						"this way. Say so plainly rather than substituting a guess."
				}
				return out, nil
			}
			// A "contains" match resolved a name by substring, so the walk may
			// have started somewhere adjacent to what was asked about.
			if res.Match == "contains" {
				out["note"] = "The starting name matched loosely; confirm " + res.From + " is what you meant."
			}
			if !res.Typed && relation != "" {
				out["note"] = "This relation declares no ends, so every edge of that type was followed " +
					"whatever it connects — the directions are the edges' own."
			}
			return out, nil
		})
}

// registerKnowledgeSearch exposes the GraphRAG knowledge base as a tool.
func registerKnowledgeSearch(svc *agent.Service, store *knowledge.Store) {
	params := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "What to look up (symptom, error string, command, concept).",
			},
		},
		"required": []string{"query"},
	}
	empties := 0
	svc.AddTool("knowledge_search",
		"Search the GraphRAG knowledge base (code, docs, recovery procedures, source error strings). "+
			"Returns the top hits plus what is one hop away along the knowledge graph (the domain's "+
			"own relations, or code edges when it declares none), so a symptom arrives with the "+
			"things it is connected to.",
		params,
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			query, _ := args["query"].(string)
			gr, err := store.SearchGraph(ctx, query, 4)
			if err != nil {
				return map[string]interface{}{"ok": false, "error": err.Error()}, nil
			}
			// Tag each hit [S1], [S2]… so the model can cite it inline. The tag's
			// source is the chunk's document id (file path / doc identifier).
			hits := make([]map[string]interface{}, 0, len(gr.Hits))
			for _, h := range gr.Hits {
				hits = append(hits, map[string]interface{}{
					"cite":     cite.Label(h.DocumentID),
					"source":   h.DocumentID,
					"content":  h.Content,
					"score":    h.Score,
					"entities": h.Entities,
				})
			}
			if len(hits) == 0 {
				// A miss must not look like a success with an empty list, and
				// must NOT carry the citation hint. Returning ok plus "cite
				// each grounded statement" for zero hits is what taught the
				// model to keep rephrasing the query and then answer from its
				// own memory in citation format — an ungrounded answer wearing
				// the shape of a grounded one, which is worse than a refusal
				// because it reads as sourced.
				empties++
				out := map[string]interface{}{
					"ok":    true,
					"hits":  hits,
					"found": false,
					"instruction": "The knowledge base has nothing on this. Do NOT answer from memory as " +
						"though it were documented, and do not write a Sources section: say plainly that " +
						"the knowledge base does not cover it, then answer only from what you can observe " +
						"with the other tools, or say you do not know.",
				}
				if empties >= maxEmptySearches {
					// Rephrasing does not conjure documents that were never
					// ingested. Five near-identical misses in one turn is a
					// pattern worth stopping, not repeating.
					out["instruction"] = "The knowledge base has returned nothing for " +
						strconv.Itoa(empties) + " consecutive queries. Stop searching it — rewording " +
						"will not find documents that were never ingested. Say the knowledge base does " +
						"not cover this, then rely on the other tools or say you do not know."
					out["stop_searching"] = true
				}
				return out, nil
			}
			empties = 0
			// Retrieval always returns SOMETHING. The relevance gate is
			// query-relative by design — raw scores vary several-fold between
			// queries, so an absolute floor either keeps everything or empties
			// the result — which means the best chunk survives however far off
			// topic it is. Ask a question the corpus does not cover and the
			// nearest unrelated document comes back looking exactly like an
			// answer, and gets cited as one.
			//
			// No threshold can fix that here, so the judgement is handed to the
			// model, which can actually tell whether a chunk is about the
			// question. It is told to check, and told what to do when the
			// answer is no. The instruction is not a suggestion in the persona
			// — it rides with the evidence it applies to.
			return map[string]interface{}{
				"ok":                true,
				"hits":              hits,
				"found":             true,
				"related_via_graph": graphNeighbors(gr.Neighbors),
				"relevance_check": "These are the closest chunks in the corpus, NOT necessarily relevant ones — " +
					"retrieval always returns its best match even when the corpus does not cover the question. " +
					"Read them before relying on them. If they are not about what was asked, say the knowledge " +
					"base does not cover it and do not cite them.",
				"citation_hint": "Cite ONLY the chunks you actually used, inline with their [cite] label, and list " +
					"just those labels under a final Sources section. Citing a chunk you did not use claims " +
					"grounding you do not have.",
			}, nil
		})
}

// graphNeighbors renders one-hop neighbours for the model, with the direction
// of each edge spelled out rather than left to be inferred.
//
// The struct carried the edge type and nothing else, so a neighbour arrived as
// {name: "tank", via: "backs"} and there was no way to tell "this resource backs
// tank" from "tank backs this resource". Those are opposite claims. The model
// guessed, and the answer read exactly as well either way — which is the failure
// mode this whole vocabulary exists to prevent, reintroduced one layer below it.
//
// Rendered as a sentence rather than a direction flag because a flag has to be
// read against a convention stated somewhere else, and everything else in this
// payload is self-describing.
func graphNeighbors(in []knowledge.Neighbor) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(in))
	for _, n := range in {
		name := n.Name
		if name == "" {
			name = n.ID
		}
		rel := fmt.Sprintf("a retrieved chunk %s %q", n.Via, name)
		if n.Direction == knowledge.DirectionIncoming {
			rel = fmt.Sprintf("%q %s a retrieved chunk", name, n.Via)
		}
		m := map[string]interface{}{
			"name":     name,
			"type":     n.Type,
			"relation": rel,
		}
		if n.Summary != "" {
			m["summary"] = n.Summary
		}
		if n.FilePath != "" {
			m["file"] = n.FilePath
		}
		out = append(out, m)
	}
	return out
}

// maxEmptySearches is how many consecutive fruitless knowledge_search calls are
// tolerated before the tool tells the model to stop.
//
// The counter lives with the tool rather than with the run: an Agent runs one
// turn at a time by contract, and the only cost of a count carried across turns
// is that a still-empty knowledge base gives up sooner — which is the right
// answer anyway. Any search that finds something resets it.
const maxEmptySearches = 3

// registerProbes wires each read-only diagnostic command as an agent tool.
func registerProbes(svc *agent.Service, list []probes.Probe, filter *safety.Filter) {
	noParams := map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
	for _, p := range list {
		pp := p // capture
		svc.AddTool(pp.Name, pp.Description, noParams,
			func(ctx context.Context, _ map[string]interface{}) (interface{}, error) {
				out, runErr := probes.Run(ctx, filter, pp.Argv)
				return map[string]interface{}{
					"command": strings.Join(pp.Argv, " "),
					"output":  out,
					"ok":      runErr == nil,
				}, nil
			})
	}
}

var codeBlock = regexp.MustCompile("(?s)```[a-zA-Z0-9]*\\n(.*?)```")

// registerSafetyLint runs the deterministic red-line wall over any fenced code
// block in the model's answer; destructive one-liners fail the lint and force a
// reframe into a guarded, confirmation-required step.
func registerSafetyLint(svc *agent.Service, filter *safety.Filter) {
	svc.RegisterOutputLint(agent.LintFunc{
		NameValue: "redline_no_destructive_oneliner",
		Fn: func(text string, _ agent.LintContext) (bool, string) {
			for _, m := range codeBlock.FindAllStringSubmatch(text, -1) {
				if v := filter.Check(m[1]); v.Blocked {
					return false, fmt.Sprintf(
						"Your answer hands the operator a destructive command in a code block "+
							"[%s: %s]. Do not present it as a runnable one-liner. Instead explain the "+
							"risk, affected volumes, and required backup, and state it needs explicit "+
							"operator confirmation plus an unlock key.", v.RuleID, v.Reason)
				}
			}
			return true, ""
		},
	})
}
