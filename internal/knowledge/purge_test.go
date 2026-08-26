package knowledge

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/cortexdb/v2/pkg/core"
	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// openPurgeTestStore wires a Store to a bare cortexdb, the way
// TestDeclaredRelationTypesTraverseRealEdges does — no embedder, no LLM.
func openPurgeTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := cortexdb.Open(cortexdb.DefaultConfig(filepath.Join(t.TempDir(), "k.db")))
	if err != nil {
		t.Fatalf("open cortexdb: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Store{db: db, tb: db.GraphRAGTools()}
}

// seedProseSource writes what IngestDoc writes for one prose document: chunk
// embeddings under the document_id, entities extracted from those chunks, and
// a relation between them.
func seedProseSource(t *testing.T, s *Store, docID string, entities []string) {
	t.Helper()
	ctx := context.Background()
	chunkID := docID + "#0"
	if err := s.db.Vector().Upsert(ctx, &core.Embedding{
		ID:         chunkID,
		Collection: "default",
		Vector:     []float32{0.1, 0.2},
		Content:    "some prose mentioning things",
		Metadata:   map[string]string{"document_id": docID},
	}); err != nil {
		t.Fatalf("upsert embedding: %v", err)
	}
	ents := make([]cortexdb.ToolEntityInput, 0, len(entities))
	for _, name := range entities {
		ents = append(ents, cortexdb.ToolEntityInput{Name: name, Type: "entity", ChunkIDs: []string{chunkID}})
	}
	if _, err := s.tb.UpsertEntities(ctx, cortexdb.ToolUpsertEntitiesRequest{DocumentID: docID, Entities: ents}); err != nil {
		t.Fatalf("upsert entities: %v", err)
	}
	if len(entities) >= 2 {
		if _, err := s.tb.UpsertRelations(ctx, cortexdb.ToolUpsertRelationsRequest{
			DocumentID: docID,
			Relations:  []cortexdb.ToolRelationInput{{From: entities[0], To: entities[1], Type: "related_to"}},
		}); err != nil {
			t.Fatalf("upsert relations: %v", err)
		}
	}
}

// TestPurgeSourceRemovesProseEntities pins the fix: purging a prose source
// removes its graph footprint, not just its vectors. The old implementation
// guessed entity ids as EntityNodeID(chunkID) — "guide.md#0" is not an entity
// name, so it deleted nothing and reported nodes: 0.
func TestPurgeSourceRemovesProseEntities(t *testing.T) {
	s := openPurgeTestStore(t)
	ctx := context.Background()

	seedProseSource(t, s, "corpus/guide.md", []string{"DRBD", "LINSTOR"})

	embN, nodeN, err := s.PurgeSource(ctx, "corpus/guide.md", false)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if embN != 1 {
		t.Fatalf("embeddings purged = %d, want 1", embN)
	}
	if nodeN == 0 {
		t.Fatal("purge removed no graph nodes — the prose subgraph was left behind")
	}
	for _, name := range []string{"DRBD", "LINSTOR"} {
		if node, _ := s.db.Graph().GetNode(ctx, cortexdb.EntityNodeID(name)); node != nil {
			t.Fatalf("entity %s survived the purge", name)
		}
	}
}

// TestPurgeSourcePrefixSparesOtherSources covers the prefix mode used for text
// sources ("<name>/..."), and that an entity shared with a surviving source is
// detached, not deleted.
func TestPurgeSourcePrefixSparesOtherSources(t *testing.T) {
	s := openPurgeTestStore(t)
	ctx := context.Background()

	seedProseSource(t, s, "corpus/a.md", []string{"DRBD", "LINSTOR"})
	seedProseSource(t, s, "other/keep.md", []string{"DRBD"})

	embN, _, err := s.PurgeSource(ctx, "corpus/", true)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if embN != 1 {
		t.Fatalf("embeddings purged = %d, want 1", embN)
	}
	// LINSTOR was corpus-only: gone. DRBD is still asserted by other/keep.md.
	if node, _ := s.db.Graph().GetNode(ctx, cortexdb.EntityNodeID("LINSTOR")); node != nil {
		t.Fatal("corpus-only entity survived")
	}
	if node, _ := s.db.Graph().GetNode(ctx, cortexdb.EntityNodeID("DRBD")); node == nil {
		t.Fatal("shared entity was deleted; other/keep.md still asserts it")
	}
	var keepEmb int
	if err := s.db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM embeddings WHERE json_extract(metadata,'$.document_id') = 'other/keep.md'`).Scan(&keepEmb); err != nil {
		t.Fatal(err)
	}
	if keepEmb != 1 {
		t.Fatal("purge touched another source's embeddings")
	}
}

// Re-ingesting a document must replace its graph, not add to it.
//
// Chunk ids are derived from the document id, so a re-ingest's vectors overwrite
// the previous ones and the chunk count never moves. Edges are not: their id
// includes both endpoints, so an extraction that resolves a name differently —
// or simply reads the text differently — writes a new edge and leaves the old
// one standing. Re-ingesting could then only add.
//
// Measured on the live SDS base: one `protects` edge from one document existed
// in three versions at once, one per ingest, two of them from readings since
// fixed. It is why "re-ingest the sources these came from" — which doctor says,
// and which is the only remedy it offers — could not work.
func TestReplacingADocumentsGraphRemovesTheOldExtraction(t *testing.T) {
	s := openPurgeTestStore(t)
	ctx := context.Background()

	seedProseSource(t, s, "corpus/guide.md", []string{"Snapshot", "Volume"})
	seedProseSource(t, s, "corpus/other.md", []string{"Node", "Gateway"})
	if edgeCount(t, s, "corpus/guide.md") == 0 {
		t.Fatal("fixture wrote no edges")
	}

	if err := s.replaceDocumentGraph(ctx, "corpus/guide.md"); err != nil {
		t.Fatalf("replace: %v", err)
	}

	if got := edgeCount(t, s, "corpus/guide.md"); got != 0 {
		t.Errorf("edges of the re-ingested document = %d, want its graph gone", got)
	}
	if got := edgeCount(t, s, "corpus/other.md"); got == 0 {
		t.Error("another document's graph was taken with it")
	}
}

// A document the store has never seen must not fail on the purge.
func TestReplacingTheGraphOfAnUnknownDocumentIsQuiet(t *testing.T) {
	if err := openPurgeTestStore(t).replaceDocumentGraph(context.Background(), "corpus/new.md"); err != nil {
		t.Errorf("replace: %v", err)
	}
}

// The purge runs only once the new chunks are safely embedded. An embedder that
// is down is the common failure — it happened twice while re-ingesting the SDS
// corpus, once mid-run with a 503 — and it must leave the document exactly as
// it was rather than stripping its graph on the way to failing.
func TestAFailedEmbedLeavesTheOldGraphInPlace(t *testing.T) {
	s := openStore(t) // its embedder address is unroutable by construction
	ctx := context.Background()
	seedProseSource(t, s, "corpus/guide.md", []string{"Snapshot", "Volume"})
	before := edgeCount(t, s, "corpus/guide.md")

	if err := s.IngestSemantic(ctx, "corpus/guide.md", "guide", "prose", nil); err == nil {
		t.Fatal("ingest succeeded with no embedder; this test proves nothing")
	}

	if got := edgeCount(t, s, "corpus/guide.md"); got != before {
		t.Errorf("edges = %d, want the %d it had before a failed ingest", got, before)
	}
}

func edgeCount(t *testing.T, s *Store, docID string) int {
	t.Helper()
	var n int
	if err := s.db.SQL().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM graph_edges
		 WHERE json_valid(properties) AND json_extract(properties,'$.document_id') = ?`,
		docID).Scan(&n); err != nil {
		t.Fatalf("count edges: %v", err)
	}
	return n
}
