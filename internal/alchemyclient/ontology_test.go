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
