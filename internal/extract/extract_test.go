package extract

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/liliang-cn/opsdoctor/internal/domain"
)

// TestWarnsOnceOnMissingOntology proves the ingest is not silent about a graph
// nothing will be able to traverse, and that it says so once per run rather than
// once per chunk.
func TestWarnsOnceOnMissingOntology(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(os.Stderr); log.SetFlags(log.LstdFlags) })

	e := New(nil, &domain.Domain{Name: "storage"})
	e.warnIfUnconstrained()
	e.warnIfUnconstrained()

	out := buf.String()
	if strings.Count(out, "declares no") != 1 {
		t.Errorf("want exactly one warning, got:\n%s", out)
	}
	for _, want := range []string{"storage", "entity_types", "relation_types"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning does not mention %q:\n%s", want, out)
		}
	}

	buf.Reset()
	full := New(nil, &domain.Domain{Name: "storage", EntityTypes: []string{"Node"}, RelationTypes: []string{"CONTAINS"}})
	full.warnIfUnconstrained()
	if buf.Len() != 0 {
		t.Errorf("warned about a domain that declares an ontology:\n%s", buf.String())
	}
}

// TestPromptConstrainsToDeclaredVocabulary keeps the closed-vocabulary case
// intact: a domain that declares an ontology still gets it verbatim.
func TestPromptConstrainsToDeclaredVocabulary(t *testing.T) {
	e := New(nil, &domain.Domain{
		Name:          "storage",
		EntityTypes:   []string{"Node", "Volume"},
		RelationTypes: []string{"DEPLOYED_ON", "CONTAINS"},
	})

	p := e.prompt("some text")
	for _, want := range []string{
		"Use ONLY these entity types: Node, Volume",
		"Use ONLY these relation types: DEPLOYED_ON, CONTAINS",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, freeVocabularyGuidance) {
		t.Errorf("prompt offers a free vocabulary despite a declared one:\n%s", p)
	}
}

// TestPromptOmitsEmptyConstraint is the fix: with nothing declared, the prompt
// used to read "Use ONLY these entity types:" followed by nothing — a rule that
// forbids every type and permits none, which the model can only ignore.
func TestPromptOmitsEmptyConstraint(t *testing.T) {
	e := New(nil, &domain.Domain{Name: "storage"})

	p := e.prompt("some text")
	if strings.Contains(p, "Use ONLY") {
		t.Errorf("prompt constrains to an empty vocabulary:\n%s", p)
	}
	if !strings.Contains(p, freeVocabularyGuidance) {
		t.Errorf("prompt does not ask for stable type names:\n%s", p)
	}
	if !strings.Contains(p, "some text") {
		t.Errorf("prompt lost the chunk:\n%s", p)
	}
}

// TestPromptConstrainsWhatItCan covers the half-declared domain: the side that
// exists is still enforced, and the guidance covers the side that does not.
func TestPromptConstrainsWhatItCan(t *testing.T) {
	e := New(nil, &domain.Domain{Name: "storage", EntityTypes: []string{"Node"}})

	p := e.prompt("some text")
	if !strings.Contains(p, "Use ONLY these entity types: Node") {
		t.Errorf("prompt dropped the declared entity types:\n%s", p)
	}
	if strings.Contains(p, "Use ONLY these relation types") {
		t.Errorf("prompt constrains relations it has no vocabulary for:\n%s", p)
	}
	if !strings.Contains(p, freeVocabularyGuidance) {
		t.Errorf("prompt does not ask for stable relation names:\n%s", p)
	}
}

// The ends the domain declared have to reach the model, because it is the only
// place a wrong direction can be prevented rather than detected.
//
// The prompt named the relation types and left the model to guess which way
// each one runs. Everything else read the ends — the ontology's link sides,
// drift, the typed walk — so a reversed edge was registered against, reported,
// and excluded from traversal, and still had to be stored first. Measured on
// the live SDS base: half of `configured_by`'s uses did not match its declared
// ends, and re-ingesting under the same prompt would only re-roll them.
func TestPromptStatesTheEndsARelationRunsBetween(t *testing.T) {
	e := New(nil, &domain.Domain{
		Name:          "storage",
		EntityTypes:   []string{"Node", "Volume", "ConfigParameter"},
		RelationTypes: []string{"configured_by", "backs"},
		Relations: []domain.Relation{{
			Name: "configured_by",
			From: domain.TypeSet{"Node", "Volume"},
			To:   domain.TypeSet{"ConfigParameter"},
		}},
	})

	p := e.prompt("some text")
	if !strings.Contains(p, "configured_by: Node|Volume -> ConfigParameter") {
		t.Errorf("prompt does not state configured_by's ends:\n%s", p)
	}
	// A relation the domain left open must still be offered, and said to be
	// open — dropping it would silently shrink the vocabulary the model may
	// use, and stating an end it does not have would be a lie.
	if !strings.Contains(p, "backs") {
		t.Errorf("prompt dropped a relation with no declared ends:\n%s", p)
	}
}

// With no ends declared anywhere, the prompt keeps its old shape: a plain list.
// Rendering an empty ends table would spend prompt budget saying nothing.
func TestPromptKeepsThePlainListWhenNoEndsAreDeclared(t *testing.T) {
	e := New(nil, &domain.Domain{
		Name:          "storage",
		EntityTypes:   []string{"Node"},
		RelationTypes: []string{"CONTAINS", "DEPLOYED_ON"},
	})

	p := e.prompt("some text")
	if !strings.Contains(p, "Use ONLY these relation types: CONTAINS, DEPLOYED_ON") {
		t.Errorf("prompt changed shape for a domain that declared no ends:\n%s", p)
	}
}

// A flag names something; it is not that thing.
//
// The WAN design doc lists WANMode, DREndpoint and WANPort as fields of the
// Resource struct — a DRBD resource configured by those parameters, which is
// exactly what the vocabulary declares. The model extracted the relation, the
// direction and the target correctly and got the subject wrong: it wrote
// `--resource`, the flag that selects a resource, in place of the resource. The
// same happened with `--controller` and the keys of the controller's config
// file. Twelve edges on the live base, every one of them right except for
// standing a flag where its entity belongs.
//
// Worth a line of prompt because this package ingests a binary's --help as a
// first-class source, so its material is full of flags sitting next to the
// things they configure.
func TestPromptSaysAFlagIsNotTheThingItNames(t *testing.T) {
	e := New(nil, &domain.Domain{
		Name:          "storage",
		EntityTypes:   []string{"DRBDResource", "ConfigParameter"},
		RelationTypes: []string{"configured_by"},
	})

	p := e.prompt("some text")
	if !strings.Contains(p, flagGuidance) {
		t.Errorf("prompt does not warn against extracting a flag as its entity:\n%s", p)
	}
}

// With no vocabulary the prompt already tells the model to invent one, and this
// rule is about placing entities the domain named. Adding it there would be
// prompt spent on a distinction the model has no types to express.
func TestPromptOmitsTheFlagRuleWithNoVocabulary(t *testing.T) {
	e := New(nil, &domain.Domain{Name: "storage"})
	if p := e.prompt("some text"); strings.Contains(p, flagGuidance) {
		t.Errorf("prompt carries the flag rule with no vocabulary declared:\n%s", p)
	}
}
