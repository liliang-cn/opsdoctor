package alchemyclient

import (
	"reflect"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

func sampleResult() alchemy.Result {
	return alchemy.Result{
		Job: "job-7",
		Chunks: []alchemy.Chunk{
			{Index: 0, Source: "guide.md#0", Text: "..."},
			{Index: 1, Source: "guide.md#1", Text: "..."},
		},
		Entities: []alchemy.Entity{
			{ID: "e1", Type: "Gateway", Name: "nfs-gw", Aliases: []string{"the NFS gateway"},
				Attributes: map[string]any{"description": "exports the pool over NFS"},
				Provenance: alchemy.Provenance{Source: "guide.md#0", Chunk: 0, Producer: alchemy.ProducerLLMExtract, Model: "gemini", Ontology: "ex@1", Confidence: 0.9}},
			{ID: "e2", Type: "Node", Name: "node-a",
				Provenance: alchemy.Provenance{Source: "guide.md#1", Chunk: 1, Producer: alchemy.ProducerLLMExtract, Ontology: "ex@1"}},
		},
		Relations: []alchemy.Relation{
			{From: "e2", To: "e1", Type: "backs",
				Provenance: alchemy.Provenance{Source: "guide.md#1", Chunk: 1, Producer: alchemy.ProducerLLMExtract, Ontology: "ex@1"}},
			{From: "e2", To: "ghost", Type: "backs",
				Provenance: alchemy.Provenance{Source: "guide.md#1", Chunk: 1, Producer: alchemy.ProducerLLMExtract}},
		},
		Violations: []alchemy.Violation{{Kind: alchemy.ViolationUnknownEntityType, Subject: "StorageClass", Detail: "not declared"}},
		Duplicates: []alchemy.Duplicate{{Subject: "nfs-gw / NFS gateway", Detail: "same thing?"}},
		Unread:     []alchemy.Unread{{Source: "guide.md#1", Reason: "model returned nothing"}},
	}
}

// alchemy names entities by a job-local id and the store names them by what
// they are called: relations cross over by name, and the chunk an entity
// was read from becomes the chunk id the store holds, so a citation lands
// on a row that exists. Everything alchemy knows about where a fact came
// from rides along as metadata the store did not have to be taught.
func TestGraphOfCarriesNamesChunksAndProvenance(t *testing.T) {
	dg := graphOf(sampleResult())

	if len(dg.Entities) != 2 {
		t.Fatalf("entities = %+v, want 2", dg.Entities)
	}
	gw := dg.Entities[0]
	if gw.Name != "nfs-gw" || gw.Type != "Gateway" || gw.Description != "exports the pool over NFS" {
		t.Errorf("entity = %+v", gw)
	}
	if !reflect.DeepEqual(gw.ChunkIDs, []string{"guide.md#0"}) {
		t.Errorf("chunk ids = %v, want the store's chunk id for alchemy chunk 0", gw.ChunkIDs)
	}
	for k, want := range map[string]string{
		MetaJob: "job-7", MetaEntityID: "e1", MetaProducer: "llm-extract", MetaStated: "false",
		MetaOntology: "ex@1", MetaModel: "gemini", MetaConfidence: "0.90", MetaAliases: `["the NFS gateway"]`,
	} {
		if got := gw.Metadata[k]; got != want {
			t.Errorf("metadata[%s] = %q, want %q", k, got, want)
		}
	}

	if len(dg.Relations) != 1 {
		t.Fatalf("relations = %+v, want the one whose ends both resolve", dg.Relations)
	}
	r := dg.Relations[0]
	if r.From != "node-a" || r.To != "nfs-gw" || r.Type != "backs" {
		t.Errorf("relation = %+v", r)
	}
	if !r.Inferred {
		t.Error("an llm-extract relation is inferred, not stated")
	}
	if !reflect.DeepEqual(r.ChunkIDs, []string{"guide.md#1"}) {
		t.Errorf("relation chunk ids = %v", r.ChunkIDs)
	}
	if r.Metadata[MetaJob] != "job-7" || !strings.Contains(r.Provenance, "job-7") {
		t.Errorf("relation provenance = %q / %v", r.Provenance, r.Metadata)
	}

	// A dangling end, a type outside the vocabulary, an undecided duplicate
	// and an unread chunk are each one line: findings, not failures.
	joined := strings.Join(dg.Notes, "\n")
	for _, want := range []string{"ghost", "StorageClass", "nfs-gw / NFS gateway", "model returned nothing"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes lack %q:\n%s", want, joined)
		}
	}
	if dg.Held != "" {
		t.Errorf("held = %q, want a clean result to be writable", dg.Held)
	}
}

// A stated fact — one a deterministic producer read rather than a model
// proposed — is not inferred.
func TestAStatedRelationIsNotInferred(t *testing.T) {
	res := sampleResult()
	res.Relations = res.Relations[:1]
	res.Relations[0].Provenance.Producer = alchemy.ProducerHuman
	dg := graphOf(res)
	if len(dg.Relations) != 1 || dg.Relations[0].Inferred {
		t.Errorf("relations = %+v, want one stated relation", dg.Relations)
	}
	if dg.Relations[0].Metadata[MetaStated] != "true" {
		t.Errorf("stated = %q", dg.Relations[0].Metadata[MetaStated])
	}
}

// The idempotency key is the document as alchemy will see it: same chunks
// under the same vocabulary, same key, so a re-run of an unchanged ingest is
// a replay on the server rather than a second bill. Any change to either is
// a new key.
func TestIdempotencyKeyFollowsContentAndVocabulary(t *testing.T) {
	a := idempotencyKey("ex@1", "guide.md", []string{"one", "two"})
	if a != idempotencyKey("ex@1", "guide.md", []string{"one", "two"}) {
		t.Error("the same document produced two keys")
	}
	for name, b := range map[string]string{
		"content":    idempotencyKey("ex@1", "guide.md", []string{"one", "three"}),
		"vocabulary": idempotencyKey("ex@2", "guide.md", []string{"one", "two"}),
		"document":   idempotencyKey("ex@1", "other.md", []string{"one", "two"}),
		"chunking":   idempotencyKey("ex@1", "guide.md", []string{"onetwo"}),
	} {
		if b == a {
			t.Errorf("changing the %s kept the key", name)
		}
	}
}
