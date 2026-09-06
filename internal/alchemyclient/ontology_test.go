package alchemyclient

import (
	"bytes"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/ontology"

	"github.com/liliang-cn/opsdoctor/internal/domain"
)

func exampleDomain() *domain.Domain {
	return &domain.Domain{
		Name:          "Example storage/infra domain",
		EntityTypes:   []string{"Cluster", "Node", "StoragePool", "Resource"},
		RelationTypes: []string{"CONTAINS", "DEPLOYED_ON", "backs"},
		Relations: []domain.Relation{
			{Name: "backs", From: domain.TypeSet{"StoragePool"}, To: domain.TypeSet{"Resource"}},
			{Name: "CONTAINS", From: domain.TypeSet{"Cluster"}, To: domain.TypeSet{"Node", "StoragePool"}},
		},
	}
}

// The ontology handed to alchemy is domain.toml, translated — and the proof
// that the translation is right is alchemy's own loader accepting it, since
// that loader is what refuses a job at CreateJob. A translation that only
// looked right would fail on the first real document.
func TestOntologyForDomainIsOneAlchemyAccepts(t *testing.T) {
	dom := exampleDomain()
	blob, id, err := OntologyFor(dom)
	if err != nil {
		t.Fatalf("OntologyFor: %v", err)
	}
	o, err := ontology.Load(bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("alchemy refuses the ontology this package built:\n%s\n%v", blob, err)
	}
	if o.ID != id {
		t.Errorf("id = %q, want %q", o.ID, id)
	}
	if !strings.HasPrefix(id, "example-storage-infra-domain@") {
		t.Errorf("id = %q, want the domain's name slugged and a version after @", id)
	}
	v, err := o.Vocabulary(ontology.PartProse)
	if err != nil {
		t.Fatalf("prose part: %v", err)
	}
	if len(v.Entities) != 4 || len(v.Relations) != 3 {
		t.Errorf("vocabulary = %d entities, %d relations; want 4 and 3", len(v.Entities), len(v.Relations))
	}
	// Ends declared in domain.toml reach alchemy as ends; a relation without
	// them stays open, which is what the domain said.
	ok, why := v.AllowsRelation("backs", "StoragePool", "Resource")
	if !ok {
		t.Errorf("backs StoragePool→Resource refused: %s", why)
	}
	if ok, _ := v.AllowsRelation("backs", "Resource", "StoragePool"); ok {
		t.Error("backs Resource→StoragePool allowed, but domain.toml declared the ends")
	}
	if ok, why := v.AllowsRelation("DEPLOYED_ON", "Resource", "Node"); !ok {
		t.Errorf("DEPLOYED_ON has no declared ends and must stay open: %s", why)
	}
}

// The version in the id is the vocabulary's fingerprint: change a type and
// the id changes, so a graph's provenance says which edit of domain.toml
// checked it. Reordering is not an edit.
func TestOntologyIDTracksTheVocabularyNotItsOrder(t *testing.T) {
	a, ida, _ := OntologyFor(exampleDomain())
	b := exampleDomain()
	b.EntityTypes = []string{"Resource", "StoragePool", "Node", "Cluster"}
	_, idb, _ := OntologyFor(b)
	if ida != idb {
		t.Errorf("reordering changed the id: %q vs %q", ida, idb)
	}
	c := exampleDomain()
	c.EntityTypes = append(c.EntityTypes, "Volume")
	_, idc, _ := OntologyFor(c)
	if idc == ida {
		t.Errorf("adding a type kept the id %q", idc)
	}
	if !bytes.Contains(a, []byte(`"prose"`)) {
		t.Errorf("ontology has no prose part:\n%s", a)
	}
}

// A domain with no vocabulary cannot be given to alchemy: it has no
// unconstrained mode, and the refusal should name the file to edit rather
// than arrive from the server as a rejected job.
func TestOntologyForAnUndeclaredDomainRefuses(t *testing.T) {
	_, _, err := OntologyFor(&domain.Domain{Name: "bare"})
	if err == nil || !strings.Contains(err.Error(), "entity_types") {
		t.Errorf("err = %v, want a refusal naming entity_types", err)
	}
}

// The three flags alchemy holds a job on are the ones that decide whether
// ingest stops. They were declared in domain.toml and dropped in translation:
// the ontology carried names and ends and nothing else, so a symmetric
// relation held every job that extracted the pair, and a corpus that
// corrected itself reported no disagreement at all.
func TestRelationFlagsReachAlchemy(t *testing.T) {
	dom := exampleDomain()
	dom.RelationTypes = append(dom.RelationTypes, "replicates_to", "runs_on")
	dom.Relations = append(dom.Relations,
		domain.Relation{Name: "replicates_to", From: domain.TypeSet{"Node"}, To: domain.TypeSet{"Node"}, BothWays: true},
		domain.Relation{Name: "runs_on", From: domain.TypeSet{"Resource"}, To: domain.TypeSet{"Node"}, AtMostOneOut: true},
	)
	blob, _, err := OntologyFor(dom)
	if err != nil {
		t.Fatalf("OntologyFor: %v", err)
	}
	o, err := ontology.Load(bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("alchemy refuses the ontology:\n%s\n%v", blob, err)
	}
	v, err := o.Vocabulary(ontology.PartProse)
	if err != nil {
		t.Fatalf("prose part: %v", err)
	}
	byName := map[string]ontology.RelationType{}
	for _, r := range v.Relations {
		byName[r.Name] = r
	}
	if !byName["replicates_to"].BothWays {
		t.Error("both_ways did not reach alchemy; a symmetric relation will hold every job that extracts the pair")
	}
	if !byName["runs_on"].AtMostOneOut {
		t.Error("at_most_one_out did not reach alchemy; a corrected fact will be stored as two")
	}
	if byName["runs_on"].AtMostOneIn || byName["backs"].BothWays {
		t.Error("a flag appeared on a relation that did not declare it")
	}
}

// Turning a flag on changes which graphs the ontology admits, so it changes
// the vocabulary's meaning — and the id has to move with it, or a graph
// extracted under the old rules claims to have been checked under the new.
func TestOntologyIDTracksTheFlags(t *testing.T) {
	_, before, _ := OntologyFor(exampleDomain())
	d := exampleDomain()
	d.Relations[0].AtMostOneIn = true
	_, after, _ := OntologyFor(d)
	if before == after {
		t.Errorf("declaring at_most_one_in kept the id %q", after)
	}
}
