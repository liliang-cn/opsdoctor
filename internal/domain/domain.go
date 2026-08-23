// Package domain is the platform's product-agnostic seam. oss-agent is a generic
// engine; each project that uses it describes ITS OWN product in a domain.toml
// config — persona, ontology vocabulary, source error patterns, read-only probes,
// and repos. No product is compiled into the platform; a worked example config
// ships under examples/.
package domain

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/liliang-cn/oss-agent/internal/probes"
	"github.com/liliang-cn/oss-agent/internal/safety"
)

// Domain bundles the product-specific knowledge that shapes the agent. It is
// loaded from a domain.toml; the engine treats it as opaque config.
type Domain struct {
	Name          string   `toml:"name"`           // human label
	Title         string   `toml:"title"`          // UI title/brand (defaults to Name)
	Persona       string   `toml:"persona"`        // agent system prompt
	EntityTypes   []string `toml:"entity_types"`   // ontology node types
	RelationTypes []string `toml:"relation_types"` // ontology edge types
	// Relations are the edge types whose ends are known. See Relation.
	Relations        []Relation        `toml:"relation"`
	ErrorPatternsRaw []string          `toml:"error_patterns"` // regex sources (group 1 = message)
	Probes           []probes.Probe    `toml:"probes"`         // read-only diagnostic commands
	Repos            []string          `toml:"repos"`          // upstream repos to ingest
	RedLines         []safety.RuleSpec `toml:"red_lines"`      // deterministic destructive-command blocks

	// Vocabulary holds vocabularies that are NOT the prose one above.
	Vocabulary Vocabulary `toml:"vocabulary"`

	// ErrorPatterns are compiled from ErrorPatternsRaw at load time.
	ErrorPatterns []*regexp.Regexp `toml:"-"`
}

// Relation is an edge type that says what may sit on each end.
//
// relation_types declares edge names and nothing else, which was all the engine
// could use: a link registered in the ontology had to name an object type on
// each side, so both ends pointed at one open "Entity" supertype standing for
// "unconstrained". That is not a neutral placeholder. cortexdb decides a
// traversal's DIRECTION by keeping only nodes of the far side's type, so a link
// whose far side is Entity keeps only nodes whose type is literally "Entity" —
// which is to say almost nothing. Every ends-aware traversal over this
// vocabulary returned the empty set, and a schema that looks complete returning
// nothing is exactly the failure the ontology was registered to prevent.
//
// Declaring ends is optional and per relation. An edge type left in
// relation_types keeps the open supertype and keeps behaving as it did: it is
// extracted, expanded and drift-checked, it just cannot be traversed by
// direction and its ends are not checked.
//
// Ends are single types, not lists, because that is what a link type is: one
// name, one pair of ends. A relation genuinely polymorphic in its source — a
// `backs` that starts at a StoragePool or at a BlockDevice — should either stay
// in relation_types, or be split into two names that mean two different things.
// Generating a name per combination is not an option: the name IS the edge type
// in the graph, and "backs__StoragePool_DRBDResource" matches no edge anything
// ever wrote.
//
// One placement trap, which is TOML's and not this schema's: [[relation]] is a
// table header, so it ends the top-level section. Written next to
// relation_types — where it reads best — it silently absorbs every top-level key
// below it, and error_patterns and repos load as keys of a relation nobody
// reads. Nothing fails. `oss-agent domain` prints the relation-ends count beside
// the others so the swallowed one shows up as a zero.
type Relation struct {
	Name string `toml:"name"`
	From string `toml:"from"`
	To   string `toml:"to"`
}

// Vocabulary partitions the ontology vocabulary by where its edges come from.
//
// The fields above (EntityTypes/RelationTypes) are the PROSE vocabulary: an LLM
// reads documentation and emits Cluster, Node, DEPLOYED_ON. They are pasted
// into the extractor's prompt ("Use ONLY these entity types"), which is exactly
// why a code vocabulary cannot live there — telling a prose extractor it may
// emit `function` and `calls` invites it to invent code structure out of
// documentation.
//
// A code vocabulary is produced by a deterministic extractor instead, and is
// consumed by the importer as an admission filter. Different provenance,
// different consumer, different list.
type Vocabulary struct {
	Code CodeVocabulary `toml:"code"`
}

// Resolution says how far an extractor got toward meaning.
const (
	// ResolutionSyntactic is name matching over a parse tree. It cannot decide
	// which of two same-named methods a call reaches, and for Go it cannot
	// derive `implements` at all, because interface satisfaction is implicit
	// and structural.
	ResolutionSyntactic = "syntactic"
	// ResolutionTyped is backed by a type checker (go/types and friends), so an
	// edge names the symbol it actually resolves to.
	ResolutionTyped = "typed"
)

// CodeVocabulary is the admission list for edges mined from source.
type CodeVocabulary struct {
	EntityTypes   []string `toml:"entity_types"`
	RelationTypes []string `toml:"relation_types"`
	// Resolution defaults to syntactic, because that is what a parse-tree
	// extractor can honestly claim. Declaring `typed` is a promise about the
	// producer, not a wish.
	Resolution string `toml:"resolution"`
}

// The default code vocabulary is Understand-Anything's own type unions
// (packages/core/src/types.ts, v2.8.x), which is what `ingest-repo` imports.
//
// Defaulted rather than required because the importer refuses a whole graph
// containing any undeclared type: a domain.toml that transcribes these by hand
// and misses one turns a working import into a total rejection, and there is
// nothing product-specific about the list to transcribe in the first place. A
// domain that declares its own list still overrides this completely — narrowing
// the admission list is a legitimate way to keep a graph small.
var (
	defaultCodeEntityTypes = []string{
		"file", "function", "class", "module", "concept",
		"config", "document", "service", "table", "endpoint",
		"pipeline", "schema", "resource",
		"domain", "flow", "step",
		"article", "entity", "topic", "claim", "source",
	}
	defaultCodeRelationTypes = []string{
		// structural
		"imports", "exports", "contains", "inherits", "implements",
		// behavioral
		"calls", "subscribes", "publishes", "middleware",
		// data flow
		"reads_from", "writes_to", "transforms", "validates",
		// dependencies
		"depends_on", "tested_by", "configures",
		// semantic
		"related", "similar_to",
		// infrastructure
		"deploys", "serves", "provisions", "triggers",
		// schema/data
		"migrates", "documents", "routes", "defines_schema",
		// domain
		"contains_flow", "flow_step", "cross_domain",
		// knowledge
		"cites", "contradicts", "builds_on", "exemplifies", "categorized_under", "authored_by",
	}
)

// AllowsCodeRelation reports whether the code vocabulary declares this edge type.
//
// Deliberately case-sensitive. The prose vocabulary shouts (CONTAINS,
// DEPENDS_ON) and the code vocabulary whispers (contains, depends_on); a
// case-insensitive compare would merge "a cluster contains a node" with "a file
// contains a function". Those share a word and nothing else, and a graph that
// conflates them answers "what is inside this?" with both at once.
func (d *Domain) AllowsCodeRelation(t string) bool {
	for _, declared := range d.Vocabulary.Code.RelationTypes {
		if declared == t {
			return true
		}
	}
	return false
}

// AllowsCodeEntity reports whether the code vocabulary declares this node type.
func (d *Domain) AllowsCodeEntity(t string) bool {
	for _, declared := range d.Vocabulary.Code.EntityTypes {
		if declared == t {
			return true
		}
	}
	return false
}

// SourceGroup is one named thing to mine out of source files.
//
// Only one group exists now: error_patterns, folded in by SourceGroups. It
// answers "what messages does this code print", which is what an operator
// reading a log needs, and log strings genuinely are not in the AST — a regex
// is the right tool for them. A configurable source_patterns list used to sit
// alongside it, mining types/operations/constants by regex; that was a
// degraded tree-sitter producing flat name lists whose relationships an LLM
// then had to guess back. Code structure now arrives as a real graph via
// `ingest-repo` (Understand-Anything → graphimport), so the regex route to it
// is gone.
type SourceGroup struct {
	// Name identifies the group and suffixes the document id
	// ("pkg/gateway/nfs.go#types").
	Name string `toml:"name"`
	// Summary opens the stored document: "<Summary> <file> (<repo>):".
	// Defaults to "Extracted from".
	Summary string `toml:"summary"`
	// Patterns are regexes whose capture group 1 is the value to keep.
	Patterns []string `toml:"patterns"`
	// MinLength drops captures shorter than this. Defaults to 3; error messages
	// want more, identifiers want less.
	MinLength int `toml:"min_length"`

	// Compiled is filled at load time.
	Compiled []*regexp.Regexp `toml:"-"`
}

// Load reads and validates a domain config from a TOML file.
func Load(path string) (*Domain, error) {
	var d Domain
	if _, err := toml.DecodeFile(path, &d); err != nil {
		return nil, fmt.Errorf("load domain %q: %w", path, err)
	}
	if d.Name == "" {
		return nil, fmt.Errorf("domain %q: missing required field 'name'", path)
	}
	if d.Persona == "" {
		return nil, fmt.Errorf("domain %q: missing required field 'persona'", path)
	}
	if d.Title == "" {
		d.Title = d.Name
	}
	switch d.Vocabulary.Code.Resolution {
	case "":
		d.Vocabulary.Code.Resolution = ResolutionSyntactic
	case ResolutionSyntactic, ResolutionTyped:
	default:
		return nil, fmt.Errorf("domain %q: vocabulary.code resolution %q must be %q or %q",
			path, d.Vocabulary.Code.Resolution, ResolutionSyntactic, ResolutionTyped)
	}
	// Each list defaults independently: a domain narrowing the node types it
	// admits should not silently lose the edge types along with them.
	if len(d.Vocabulary.Code.EntityTypes) == 0 {
		d.Vocabulary.Code.EntityTypes = append([]string(nil), defaultCodeEntityTypes...)
	}
	if len(d.Vocabulary.Code.RelationTypes) == 0 {
		d.Vocabulary.Code.RelationTypes = append([]string(nil), defaultCodeRelationTypes...)
	}
	if err := d.compileRelations(path); err != nil {
		return nil, err
	}
	for _, p := range d.ErrorPatternsRaw {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("domain %q: invalid error_pattern %q: %w", path, p, err)
		}
		d.ErrorPatterns = append(d.ErrorPatterns, re)
	}
	return &d, nil
}

// compileRelations validates the ends-aware relations and folds their names into
// RelationTypes, so everything downstream sees one edge vocabulary and does not
// have to know which half of the file a name came from.
//
// The ends are validated against entity_types rather than accepted on trust,
// because cortexdb refuses a whole schema whose link side names an object type
// it does not declare — and registration is best-effort, so the refusal is a
// single log line and then a knowledge base that silently has no ontology at
// all. A typo in a `to =` should cost the load, loudly, not the vocabulary,
// quietly.
func (d *Domain) compileRelations(path string) error {
	declared := make(map[string]struct{}, len(d.EntityTypes))
	for _, t := range d.EntityTypes {
		declared[strings.ToLower(strings.TrimSpace(t))] = struct{}{}
	}
	known := func(field, name, t string) error {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("domain %q: relation %q needs a %s", path, name, field)
		}
		if _, ok := declared[strings.ToLower(strings.TrimSpace(t))]; !ok {
			return fmt.Errorf("domain %q: relation %q has %s = %q, which entity_types does not declare",
				path, name, field, t)
		}
		return nil
	}

	seen := make(map[string]struct{}, len(d.Relations))
	for i := range d.Relations {
		r := &d.Relations[i]
		r.Name = strings.TrimSpace(r.Name)
		r.From = strings.TrimSpace(r.From)
		r.To = strings.TrimSpace(r.To)
		if r.Name == "" {
			return fmt.Errorf("domain %q: a [[relation]] has no name", path)
		}
		key := strings.ToLower(r.Name)
		if _, dup := seen[key]; dup {
			// Two [[relation]] blocks for one name are two different answers to
			// "what sits on each end", and only one can be registered. Which one
			// would be decided by file order.
			return fmt.Errorf("domain %q: relation %q is declared twice with ends", path, r.Name)
		}
		seen[key] = struct{}{}
		if err := known("from", r.Name, r.From); err != nil {
			return err
		}
		if err := known("to", r.Name, r.To); err != nil {
			return err
		}
	}

	// Listing a name in relation_types AND giving it ends below is the natural
	// way to write this file — the flat list is the vocabulary, the blocks
	// refine part of it — so the two are merged rather than treated as a
	// conflict.
	for _, r := range d.Relations {
		if _, listed := indexOfFold(d.RelationTypes, r.Name); !listed {
			d.RelationTypes = append(d.RelationTypes, r.Name)
		}
	}
	return nil
}

// indexOfFold finds a case-insensitive match, which is how every other
// comparison over this vocabulary is done: the spelling in the graph is
// whatever the extracting model emitted.
func indexOfFold(haystack []string, needle string) (int, bool) {
	for i, s := range haystack {
		if strings.EqualFold(s, needle) {
			return i, true
		}
	}
	return 0, false
}

// SourceGroups returns every group to mine from source files — just the
// error_patterns group. See the SourceGroup comment for why it is alone.
func (d *Domain) SourceGroups() []SourceGroup {
	var out []SourceGroup
	if len(d.ErrorPatterns) > 0 {
		out = append(out, SourceGroup{
			Name:      "errors",
			Summary:   "Error and log messages emitted by",
			Compiled:  d.ErrorPatterns,
			MinLength: 8, // a message shorter than this is a fragment, not a line
		})
	}
	return out
}
