// Package alchemyclient reads prose through alchemy.
//
// alchemy is the extraction service: it reads a document under a declared
// vocabulary, keeps the provenance of every entity and edge, reports what it
// could not place, and holds the job when two sources contradict each other.
// This package is the store's side of that: one document becomes one job of
// its chunks, and the job's result becomes the graph the store writes.
//
// It does not use alchemy's own CortexDB connector. That connector namespaces
// every node by the run that produced it — right for a graph loaded once,
// wrong for a knowledge base ingested a document at a time, where the same
// gateway named in forty documents must be one node. The store's own writer
// keeps that identity; alchemy's provenance rides along as metadata.
package alchemyclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/liliang-cn/alchemy/pkg/wire"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	insecurecreds "google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/liliang-cn/opsdoctor/internal/domain"
	"github.com/liliang-cn/opsdoctor/internal/knowledge"
)

// Endpoint is an OpenAI-compatible model alchemy calls on the job's behalf.
type Endpoint struct {
	Model   string
	BaseURL string
	APIKey  string
}

// Config is how to reach alchemy and what to have it read with.
type Config struct {
	// Addr is host:port of the gRPC service. TLS is off unless asked for:
	// alchemy is reached over a LAN or a tunnel, and a plaintext default is
	// what every deployment so far has been.
	Addr  string
	Token string
	TLS   bool
	// LLM is the model alchemy extracts with. No embedder is sent: the store
	// embeds its own chunks, and a second set of vectors would be a bill for
	// nothing it can read.
	LLM Endpoint

	// PollInterval and PollTimeout bound the wait for a job whose event
	// stream ended before its terminal state. Zero takes the defaults.
	PollInterval time.Duration
	PollTimeout  time.Duration
}

const (
	defaultPollInterval = 2 * time.Second
	defaultPollTimeout  = 2 * time.Hour
	uploadFrame         = 64 << 10
	// wholeBudget is the chunking size a source is read whole under. The
	// store's chunks are 1200 bytes; alchemy estimates tokens and refuses a
	// whole read over the size rather than splitting, so the budget sits
	// well above any chunk the store will send.
	wholeBudget = 4000
)

// api is the part of alchemy's client this package uses, narrowed so a test
// can stand in for it.
type api interface {
	UploadSource(ctx context.Context, opts ...grpc.CallOption) (grpc.ClientStreamingClient[alchemyv1.SourceChunk, alchemyv1.Source], error)
	CreateJob(ctx context.Context, in *alchemyv1.CreateJobRequest, opts ...grpc.CallOption) (*alchemyv1.Job, error)
	WatchJob(ctx context.Context, in *alchemyv1.WatchJobRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[alchemyv1.JobEvent], error)
	GetJob(ctx context.Context, in *alchemyv1.GetJobRequest, opts ...grpc.CallOption) (*alchemyv1.Job, error)
	GetResult(ctx context.Context, in *alchemyv1.GetResultRequest, opts ...grpc.CallOption) (*alchemyv1.Result, error)
	ListFindings(ctx context.Context, in *alchemyv1.ListFindingsRequest, opts ...grpc.CallOption) (*alchemyv1.Findings, error)
}

// Extractor is a knowledge.DocumentExtractor backed by alchemy.
type Extractor struct {
	api   api
	conn  *grpc.ClientConn
	token string
	cfg   Config

	ontology   []byte
	OntologyID string
}

// Dial connects to alchemy and prepares the domain's ontology. The
// connection is lazy: an unreachable server fails the first document, not
// this call, and that failure is logged per document by the store.
func Dial(cfg Config, dom *domain.Domain) (*Extractor, error) {
	if strings.TrimSpace(cfg.Addr) == "" {
		return nil, errors.New("alchemy: no address")
	}
	creds := credentials.NewClientTLSFromCert(nil, "")
	if !cfg.TLS {
		creds = insecurecreds.NewCredentials()
	}
	conn, err := grpc.NewClient(cfg.Addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("alchemy: dial %s: %w", cfg.Addr, err)
	}
	ex, err := New(alchemyv1.NewAlchemyClient(conn), cfg, dom)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	ex.conn = conn
	return ex, nil
}

// New builds an Extractor over an existing client.
func New(client api, cfg Config, dom *domain.Domain) (*Extractor, error) {
	blob, id, err := OntologyFor(dom)
	if err != nil {
		return nil, err
	}
	if cfg.LLM.Model == "" || cfg.LLM.BaseURL == "" {
		return nil, errors.New("alchemy: no LLM to extract with; set the LLM base URL and model")
	}
	return &Extractor{api: client, token: cfg.Token, cfg: cfg, ontology: blob, OntologyID: id}, nil
}

// Close releases the connection, if Dial made one.
func (e *Extractor) Close() error {
	if e.conn == nil {
		return nil
	}
	return e.conn.Close()
}

// Ping asks alchemy for a job that does not exist. NotFound is the healthy
// answer — it means the server was reached and the token was accepted.
func (e *Extractor) Ping(ctx context.Context) error {
	_, err := e.api.GetJob(e.auth(ctx), &alchemyv1.GetJobRequest{JobId: "opsdoctor-ping"})
	switch status.Code(err) {
	case codes.OK, codes.NotFound:
		return nil
	}
	return e.explain(err)
}

func (e *Extractor) auth(ctx context.Context) context.Context {
	if e.token == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+e.token)
}

// explain turns the two failures that repeat for every document into
// sentences that name them, and leaves the rest as they came.
func (e *Extractor) explain(err error) error {
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied:
		return fmt.Errorf("alchemy at %s refused the token (OPSDOCTOR_ALCHEMY_TOKEN): %w", e.cfg.Addr, err)
	case codes.Unavailable:
		return fmt.Errorf("alchemy at %s is unreachable: %w", e.cfg.Addr, err)
	}
	return err
}

// ExtractDocument runs one document through alchemy as one job of its chunks.
func (e *Extractor) ExtractDocument(ctx context.Context, docID string, chunks []knowledge.Chunk) (*knowledge.DocumentGraph, error) {
	if len(chunks) == 0 {
		return &knowledge.DocumentGraph{}, nil
	}
	ids := make([]string, 0, len(chunks))
	texts := make([]string, 0, len(chunks))
	for _, c := range chunks {
		id, err := e.upload(ctx, c)
		if err != nil {
			return nil, e.explain(err)
		}
		ids = append(ids, id)
		texts = append(texts, c.Text)
	}
	job, err := e.api.CreateJob(e.auth(ctx), &alchemyv1.CreateJobRequest{
		SourceIds: ids,
		Ontology:  string(e.ontology),
		Models: &alchemyv1.Models{Llm: &alchemyv1.ModelEndpoint{
			Name: e.cfg.LLM.Model, Endpoint: e.cfg.LLM.BaseURL, ApiKey: e.cfg.LLM.APIKey,
		}},
		Chunking:       &alchemyv1.Chunking{Strategy: "whole", Size: wholeBudget},
		Part:           "prose",
		IdempotencyKey: idempotencyKey(e.OntologyID, docID, texts),
	})
	if err != nil {
		return nil, fmt.Errorf("alchemy: create job for %s: %w", docID, e.explain(err))
	}
	jobID := job.GetId()
	state, jobErr, err := e.watch(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if !terminal(state) {
		if state, jobErr, err = e.poll(ctx, jobID); err != nil {
			return nil, err
		}
	}
	switch state {
	case alchemyv1.JobState_JOB_STATE_SUCCEEDED:
		res, err := e.api.GetResult(e.auth(ctx), &alchemyv1.GetResultRequest{JobId: jobID})
		if err != nil {
			return nil, fmt.Errorf("alchemy: result of job %s: %w", jobID, e.explain(err))
		}
		graph := wire.ResultFromProto(res)
		if graph.Job == "" {
			graph.Job = jobID
		}
		return graphOf(graph), nil
	case alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW:
		return &knowledge.DocumentGraph{Held: e.heldReason(ctx, jobID)}, nil
	case alchemyv1.JobState_JOB_STATE_FAILED:
		// The event stream says a job failed and not why; the job record does.
		if jobErr == "" {
			if job, err := e.api.GetJob(e.auth(ctx), &alchemyv1.GetJobRequest{JobId: jobID}); err == nil {
				jobErr = job.GetError()
			}
		}
		if jobErr == "" {
			jobErr = "the job failed and gave no reason"
		}
		return nil, fmt.Errorf("alchemy: job %s failed: %s", jobID, jobErr)
	default:
		return nil, fmt.Errorf("alchemy: job %s ended %s", jobID, state)
	}
}

func (e *Extractor) upload(ctx context.Context, c knowledge.Chunk) (string, error) {
	st, err := e.api.UploadSource(e.auth(ctx))
	if err != nil {
		return "", fmt.Errorf("alchemy: upload %s: %w", c.ID, err)
	}
	data := []byte(c.Text)
	for off := 0; off < len(data) || off == 0; off += uploadFrame {
		end := min(off+uploadFrame, len(data))
		frame := &alchemyv1.SourceChunk{Data: data[off:end]}
		if off == 0 {
			frame.Name = c.ID
			frame.Kind = alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT
			frame.MediaType = "text/markdown"
		}
		if err := st.Send(frame); err != nil {
			return "", fmt.Errorf("alchemy: upload %s: %w", c.ID, err)
		}
		if len(data) == 0 {
			break
		}
	}
	src, err := st.CloseAndRecv()
	if err != nil {
		return "", fmt.Errorf("alchemy: upload %s: %w", c.ID, err)
	}
	return src.GetId(), nil
}

// watch follows the job's events to a terminal state. A stream that ends
// early hands over to poll; a stream error is not the job's error.
func (e *Extractor) watch(ctx context.Context, jobID string) (alchemyv1.JobState, string, error) {
	last := alchemyv1.JobState_JOB_STATE_UNSPECIFIED
	st, err := e.api.WatchJob(e.auth(ctx), &alchemyv1.WatchJobRequest{JobId: jobID})
	if err != nil {
		return last, "", fmt.Errorf("alchemy: watch job %s: %w", jobID, e.explain(err))
	}
	for {
		ev, err := st.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() == nil {
				return last, "", nil
			}
			return last, "", ctx.Err()
		}
		if s := ev.GetState(); s != alchemyv1.JobState_JOB_STATE_UNSPECIFIED {
			last = s
		}
		if terminal(last) {
			return last, "", nil
		}
	}
}

func (e *Extractor) poll(ctx context.Context, jobID string) (alchemyv1.JobState, string, error) {
	every, limit := e.cfg.PollInterval, e.cfg.PollTimeout
	if every <= 0 {
		every = defaultPollInterval
	}
	if limit <= 0 {
		limit = defaultPollTimeout
	}
	deadline := time.Now().Add(limit)
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		job, err := e.api.GetJob(e.auth(ctx), &alchemyv1.GetJobRequest{JobId: jobID})
		if err != nil {
			return alchemyv1.JobState_JOB_STATE_UNSPECIFIED, "", fmt.Errorf("alchemy: poll job %s: %w", jobID, e.explain(err))
		}
		if s := job.GetState(); terminal(s) {
			return s, job.GetError(), nil
		}
		if time.Now().After(deadline) {
			return alchemyv1.JobState_JOB_STATE_UNSPECIFIED, "", fmt.Errorf("alchemy: job %s has not finished after %s", jobID, limit)
		}
		select {
		case <-ctx.Done():
			return alchemyv1.JobState_JOB_STATE_UNSPECIFIED, "", ctx.Err()
		case <-tick.C:
		}
	}
}

// heldReason names the job and its first open conflict, so the store's log
// line says what a person has to rule on.
func (e *Extractor) heldReason(ctx context.Context, jobID string) string {
	reason := fmt.Sprintf("held by alchemy job %s", jobID)
	found, err := e.api.ListFindings(e.auth(ctx), &alchemyv1.ListFindingsRequest{JobId: jobID})
	if err != nil {
		return reason + " (findings unavailable: " + err.Error() + ")"
	}
	var conflicts []string
	for _, it := range found.GetItems() {
		if it.GetKind() == alchemyv1.ReviewKind_REVIEW_KIND_CONFLICT {
			conflicts = append(conflicts, it.GetSubject())
		}
	}
	if len(conflicts) == 0 {
		return reason
	}
	return fmt.Sprintf("%s: %d conflict(s), first: %s", reason, len(conflicts), conflicts[0])
}

func terminal(s alchemyv1.JobState) bool {
	switch s {
	case alchemyv1.JobState_JOB_STATE_SUCCEEDED, alchemyv1.JobState_JOB_STATE_FAILED,
		alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW, alchemyv1.JobState_JOB_STATE_EXPIRED,
		alchemyv1.JobState_JOB_STATE_CANCELLED:
		return true
	}
	return false
}
