package knowledge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"strings"
	"testing"

	cortexdb "github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// stubExtractor answers one document with whatever it was built to say.
type stubExtractor struct {
	graph *DocumentGraph
	err   error
	got   []Chunk
	docID string
}

func (s *stubExtractor) ExtractDocument(_ context.Context, docID string, chunks []Chunk) (*DocumentGraph, error) {
	s.docID, s.got = docID, chunks
	return s.graph, s.err
}

// The graph write is the extractor's to shape: an extractor that says where an
// entity came from (metadata, chunk ids, an inferred flag on an edge) must
// find those on the node and edge it asked for. This is the seam alchemy
// plugs into, so what crosses it is what a provenance-carrying extractor has
// to say.
func TestIngestWritesTheGraphTheExtractorHandsBack(t *testing.T) {
	s := openStore(t, WithOntology("test", []string{"Gateway", "Node"}, []string{"backs"}))
	ex := &stubExtractor{graph: &DocumentGraph{
		Entities: []cortexdb.ToolEntityInput{
			{Name: "nfs-gw", Type: "Gateway", ChunkIDs: []string{"doc#0"},
				Metadata: map[string]string{"alchemy_job": "job-1", "alchemy_stated": "false"}},
			{Name: "node-a", Type: "Node", ChunkIDs: []string{"doc#0"}},
		},
		Relations: []cortexdb.ToolRelationInput{
			{From: "node-a", To: "nfs-gw", Type: "backs", Inferred: true, ChunkIDs: []string{"doc#0"},
				Metadata: map[string]string{"alchemy_job": "job-1"}},
		},
	}}
	// The embedder at 127.0.0.1:1 refuses, so vectors fail and ingest stops
	// before extraction: prove the seam with the graph path alone.
	if err := s.writeDocumentGraph(context.Background(), "doc", ex.graph); err != nil {
		t.Fatalf("write graph: %v", err)
	}

	res, err := s.tb.FindNodes(context.Background(), cortexdb.ToolFindNodesRequest{Names: []string{"nfs-gw"}, Limit: 1})
	if err != nil || len(res.Matches) == 0 || len(res.Matches[0].Nodes) == 0 {
		t.Fatalf("nfs-gw not written: %v %+v", err, res)
	}
	n := res.Matches[0].Nodes[0]
	if n.NodeType != "Gateway" {
		t.Errorf("type = %q, want Gateway", n.NodeType)
	}
	if got, _ := n.Properties["alchemy_job"].(string); got != "job-1" {
		t.Errorf("metadata alchemy_job = %q, want job-1: the provenance the extractor stated must reach the node (props %v)", got, n.Properties)
	}
	edges, err := s.db.Graph().EdgeShapes(context.Background())
	if err != nil {
		t.Fatalf("edges: %v", err)
	}
	backs := 0
	for _, e := range edges {
		if e.EdgeType == "backs" && e.FromType == "Node" && e.ToType == "Gateway" {
			backs += e.Count
		}
	}
	if backs != 1 {
		t.Errorf("edges = %+v, want one Node -backs-> Gateway", edges)
	}
}

// A document alchemy holds for review has vectors and no graph. Writing the
// half of a graph the reviewer has not seen would put the disputed edge in
// the store as a fact, which is the one thing holding exists to prevent —
// and doing it quietly would be worse than not holding at all, so it is
// said on the log and counted.
func TestAHeldDocumentIsSaidAndCountedAndNotWritten(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(os.Stderr); log.SetFlags(log.LstdFlags) })

	s := openStore(t)
	before := IngestHeld()
	dg := &DocumentGraph{
		Held:     "held by alchemy job job-9: 1 conflict, first: nfs-gw.role (attribute)",
		Entities: []cortexdb.ToolEntityInput{{Name: "nfs-gw", Type: "Gateway"}},
	}
	if err := s.writeDocumentGraph(context.Background(), "doc", dg); err != nil {
		t.Fatalf("held is not an error: %v", err)
	}
	if IngestHeld() != before+1 {
		t.Errorf("held count = %d, want %d", IngestHeld(), before+1)
	}
	if !strings.Contains(buf.String(), "job-9") || !strings.Contains(buf.String(), "nfs-gw.role") {
		t.Errorf("log does not name the job and the conflict:\n%s", buf.String())
	}
	res, err := s.tb.FindNodes(context.Background(), cortexdb.ToolFindNodesRequest{Names: []string{"nfs-gw"}, Limit: 1})
	if err == nil && len(res.Matches) > 0 && len(res.Matches[0].Nodes) > 0 {
		t.Error("a held document's entities were written")
	}
}

// An extractor that fails is counted as an extraction failure, exactly as the
// per-chunk LLM path always was, and the document keeps its vectors.
func TestAnExtractorErrorIsCountedNotFatal(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(os.Stderr); log.SetFlags(log.LstdFlags) })

	s := openStore(t)
	ex := &stubExtractor{err: errors.New("alchemy: job failed: model said no")}
	extractLogged.Store(false)
	before, _ := IngestFailures()
	if err := s.extractAndWrite(context.Background(), "doc", []Chunk{{ID: "doc#0", Text: "x"}}, ex); err != nil {
		t.Fatalf("an extraction failure must not fail ingest: %v", err)
	}
	if got, _ := IngestFailures(); got != before+1 {
		t.Errorf("extraction failures = %d, want %d", got, before+1)
	}
	if !strings.Contains(buf.String(), "model said no") {
		t.Errorf("the failure was not said:\n%s", buf.String())
	}
	if ex.docID != "doc" || len(ex.got) != 1 || ex.got[0].ID != "doc#0" {
		t.Errorf("extractor got docID=%q chunks=%+v", ex.docID, ex.got)
	}
}

// PerChunk wraps the LLM extractor into the same seam. A nil extractor is a
// nil seam, not an interface holding a nil pointer that every caller would
// then have to know to test for.
func TestPerChunkOfNothingIsNothing(t *testing.T) {
	if ex := PerChunk(nil); ex != nil {
		t.Errorf("PerChunk(nil) = %#v, want a nil interface", ex)
	}
}

// A re-ingest whose extractor fails keeps the document's previous graph. The
// old order purged first and extracted second, so an alchemy that was down
// turned a refresh into a wipe: two documents, eighteen nodes, zero after,
// and "ingested" on stdout.
func TestAFailedReExtractionKeepsThePreviousGraph(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	s := openStore(t)
	ctx := context.Background()
	first := &DocumentGraph{Entities: []cortexdb.ToolEntityInput{{Name: "nfs-gw", Type: "Gateway", ChunkIDs: []string{"doc#0"}}}}
	if err := s.writeDocumentGraph(ctx, "doc", first); err != nil {
		t.Fatal(err)
	}
	chunks := []Chunk{{ID: "doc#0", Text: "x"}}
	for name, ex := range map[string]DocumentExtractor{
		"failed": &stubExtractor{err: errors.New("alchemy is down")},
		"held":   &stubExtractor{graph: &DocumentGraph{Held: "held by alchemy job j"}},
	} {
		if err := s.extractAndWrite(ctx, "doc", chunks, ex); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		res, err := s.tb.FindNodes(ctx, cortexdb.ToolFindNodesRequest{Names: []string{"nfs-gw"}, Limit: 1})
		if err != nil || len(res.Matches) == 0 || len(res.Matches[0].Nodes) == 0 {
			t.Errorf("%s: the previous graph was stripped", name)
		}
	}
	// A new graph replaces the old one, so nothing from a superseded version
	// of the document lingers beside the current one.
	second := &stubExtractor{graph: &DocumentGraph{Entities: []cortexdb.ToolEntityInput{{Name: "node-a", Type: "Node", ChunkIDs: []string{"doc#0"}}}}}
	if err := s.extractAndWrite(ctx, "doc", chunks, second); err != nil {
		t.Fatal(err)
	}
	res, _ := s.tb.FindNodes(ctx, cortexdb.ToolFindNodesRequest{Names: []string{"nfs-gw"}, Limit: 1})
	if len(res.Matches) > 0 && len(res.Matches[0].Nodes) > 0 {
		t.Error("the superseded graph lingered after a successful re-extraction")
	}
}
