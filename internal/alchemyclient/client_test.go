package alchemyclient

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"

	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/liliang-cn/opsdoctor/internal/knowledge"
)

// fake is enough of an alchemy server to drive one job through the client:
// it records what was uploaded and asked for, and answers with the terminal
// state it was built with.
type fake struct {
	alchemyv1.UnimplementedAlchemyServer

	token    string
	terminal alchemyv1.JobState
	result   *alchemyv1.Result
	jobError string
	findings *alchemyv1.Findings

	mu       sync.Mutex
	sources  []*alchemyv1.SourceChunk // first frame of each upload
	creates  []*alchemyv1.CreateJobRequest
	unauthed int
}

func (f *fake) authed(ctx context.Context) bool {
	if f.token == "" {
		return true
	}
	md, _ := metadata.FromIncomingContext(ctx)
	for _, v := range md.Get("authorization") {
		if v == "Bearer "+f.token {
			return true
		}
	}
	f.mu.Lock()
	f.unauthed++
	f.mu.Unlock()
	return false
}

func (f *fake) UploadSource(st grpc.ClientStreamingServer[alchemyv1.SourceChunk, alchemyv1.Source]) error {
	if !f.authed(st.Context()) {
		return status.Error(codes.Unauthenticated, "no token")
	}
	var first *alchemyv1.SourceChunk
	size := int64(0)
	for {
		c, err := st.Recv()
		if err != nil {
			break
		}
		if first == nil {
			first = c
		}
		size += int64(len(c.GetData()))
	}
	f.mu.Lock()
	f.sources = append(f.sources, first)
	n := len(f.sources)
	f.mu.Unlock()
	return st.SendAndClose(&alchemyv1.Source{Id: "src-" + strings.Repeat("x", n), Name: first.GetName(), Size: size})
}

func (f *fake) CreateJob(ctx context.Context, req *alchemyv1.CreateJobRequest) (*alchemyv1.Job, error) {
	if !f.authed(ctx) {
		return nil, status.Error(codes.Unauthenticated, "no token")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates = append(f.creates, req)
	return &alchemyv1.Job{Id: "job-1"}, nil
}

func (f *fake) WatchJob(_ *alchemyv1.WatchJobRequest, st grpc.ServerStreamingServer[alchemyv1.JobEvent]) error {
	_ = st.Send(&alchemyv1.JobEvent{State: alchemyv1.JobState_JOB_STATE_RUNNING, Stage: "extract"})
	return st.Send(&alchemyv1.JobEvent{State: f.terminal, Stage: "done"})
}

func (f *fake) GetJob(_ context.Context, req *alchemyv1.GetJobRequest) (*alchemyv1.Job, error) {
	return &alchemyv1.Job{Id: req.GetJobId(), Error: f.jobError}, nil
}

func (f *fake) GetResult(_ context.Context, _ *alchemyv1.GetResultRequest) (*alchemyv1.Result, error) {
	if f.terminal != alchemyv1.JobState_JOB_STATE_SUCCEEDED {
		return nil, status.Error(codes.FailedPrecondition, "not finished")
	}
	return f.result, nil
}

func (f *fake) ListFindings(_ context.Context, _ *alchemyv1.ListFindingsRequest) (*alchemyv1.Findings, error) {
	if f.findings == nil {
		return &alchemyv1.Findings{}, nil
	}
	return f.findings, nil
}

func serve(t *testing.T, f *fake) *Extractor {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	alchemyv1.RegisterAlchemyServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(); srv.Stop() })
	ex, err := New(alchemyv1.NewAlchemyClient(conn), Config{Token: f.token, LLM: Endpoint{Model: "m", BaseURL: "http://llm", APIKey: "k"}}, exampleDomain())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ex
}

func chunks() []knowledge.Chunk {
	return []knowledge.Chunk{{ID: "guide.md#0", Text: "The pool backs the resource."}, {ID: "guide.md#1", Text: "node-a hosts nfs-gw."}}
}

// One document is one job: every chunk the store embedded goes up as its
// own source named by the chunk's id, read whole, so alchemy's chunk and the
// store's chunk are the same span and a citation resolves. The job carries
// the domain's ontology, the prose part, the LLM the caller configured and
// a key that makes a re-run a replay.
func TestADocumentBecomesOneJobOfItsChunks(t *testing.T) {
	f := &fake{token: "secret", terminal: alchemyv1.JobState_JOB_STATE_SUCCEEDED, result: &alchemyv1.Result{
		Job:    "job-1",
		Chunks: []*alchemyv1.Chunk{{Index: 0, Source: "guide.md#0"}, {Index: 1, Source: "guide.md#1"}},
		Entities: []*alchemyv1.Entity{
			{Id: "e1", Type: "StoragePool", Name: "pool-a", Provenance: &alchemyv1.Provenance{Source: "guide.md#0", Chunk: 0, Producer: alchemyv1.Producer_PRODUCER_LLM_EXTRACT}},
			{Id: "e2", Type: "Resource", Name: "r0", Provenance: &alchemyv1.Provenance{Source: "guide.md#0", Chunk: 0, Producer: alchemyv1.Producer_PRODUCER_LLM_EXTRACT}},
		},
		Relations: []*alchemyv1.Relation{{From: "e1", To: "e2", Type: "backs", Provenance: &alchemyv1.Provenance{Source: "guide.md#0", Chunk: 0, Producer: alchemyv1.Producer_PRODUCER_LLM_EXTRACT}}},
	}}
	ex := serve(t, f)

	dg, err := ex.ExtractDocument(context.Background(), "guide.md", chunks())
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if f.unauthed != 0 {
		t.Errorf("%d calls arrived without the bearer token", f.unauthed)
	}
	if len(f.sources) != 2 || f.sources[0].GetName() != "guide.md#0" || f.sources[1].GetName() != "guide.md#1" {
		t.Errorf("uploaded sources = %v, want one per chunk named by its id", f.sources)
	}
	if f.sources[0].GetKind() != alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT || f.sources[0].GetMediaType() != "text/markdown" {
		t.Errorf("source kind/media = %v/%q", f.sources[0].GetKind(), f.sources[0].GetMediaType())
	}
	if len(f.creates) != 1 {
		t.Fatalf("creates = %d, want one job", len(f.creates))
	}
	req := f.creates[0]
	if len(req.GetSourceIds()) != 2 {
		t.Errorf("job sources = %v, want both", req.GetSourceIds())
	}
	if req.GetPart() != "prose" || req.GetChunking().GetStrategy() != "whole" {
		t.Errorf("part/chunking = %q/%q, want prose read whole", req.GetPart(), req.GetChunking().GetStrategy())
	}
	if !strings.Contains(req.GetOntology(), `"StoragePool"`) {
		t.Errorf("ontology does not carry the domain's types:\n%s", req.GetOntology())
	}
	if req.GetModels().GetLlm().GetName() != "m" || req.GetModels().GetLlm().GetEndpoint() != "http://llm" {
		t.Errorf("llm = %+v", req.GetModels().GetLlm())
	}
	if req.GetModels().GetEmbedder() != nil {
		t.Error("an embedder was sent; the store embeds its own chunks")
	}
	if req.GetIdempotencyKey() == "" {
		t.Error("no idempotency key")
	}
	if len(dg.Entities) != 2 || len(dg.Relations) != 1 || dg.Held != "" {
		t.Errorf("graph = %+v", dg)
	}
	if dg.Entities[0].Metadata[MetaJob] != "job-1" {
		t.Errorf("entity job = %q", dg.Entities[0].Metadata[MetaJob])
	}
}

// A held job is not an error and not a graph: the document comes back
// marked held with the conflict named, so the store can say why nothing
// was written.
func TestAHeldJobComesBackHeldNamingTheConflict(t *testing.T) {
	f := &fake{terminal: alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW, findings: &alchemyv1.Findings{
		Holding: 1,
		Items: []*alchemyv1.ReviewItem{
			{Id: "conflict/entity_attributes/1", Kind: alchemyv1.ReviewKind_REVIEW_KIND_CONFLICT, Subject: "nfs-gw.role", Summary: "guide says primary, notes say standby"},
			{Id: "violation/1", Kind: alchemyv1.ReviewKind_REVIEW_KIND_VIOLATION, Subject: "StorageClass"},
		},
	}}
	ex := serve(t, f)
	dg, err := ex.ExtractDocument(context.Background(), "guide.md", chunks())
	if err != nil {
		t.Fatalf("held is not an error: %v", err)
	}
	if dg.Held == "" || !strings.Contains(dg.Held, "job-1") || !strings.Contains(dg.Held, "nfs-gw.role") {
		t.Errorf("held = %q, want the job and the conflict's subject", dg.Held)
	}
	if len(dg.Entities) != 0 {
		t.Errorf("a held document carried entities: %+v", dg.Entities)
	}
}

// A failed job is an extraction failure with the server's reason in it.
func TestAFailedJobIsAnErrorWithTheReason(t *testing.T) {
	f := &fake{terminal: alchemyv1.JobState_JOB_STATE_FAILED, jobError: "model: 429 from the gateway"}
	ex := serve(t, f)
	_, err := ex.ExtractDocument(context.Background(), "guide.md", chunks())
	if err == nil || !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "job-1") {
		t.Errorf("err = %v, want the job named and the server's reason", err)
	}
}

// A wrong token is the one failure that will repeat for every document, so
// it must say what it is rather than arrive as forty "extraction failed".
func TestAWrongTokenSaysSo(t *testing.T) {
	f := &fake{token: "right", terminal: alchemyv1.JobState_JOB_STATE_SUCCEEDED}
	ex := serve(t, f)
	ex.token = "wrong"
	_, err := ex.ExtractDocument(context.Background(), "guide.md", chunks())
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "token") {
		t.Errorf("err = %v, want it to name the token", err)
	}
}
