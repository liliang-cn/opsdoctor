package agents

import (
	"context"
	"sync"

	agdomain "github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// SwappableLLM is the seam that lets the model change without a restart.
//
// agent-go holds its generator in a bare field written once at construction and
// read from twenty-odd places; the one field it does allow to change at runtime
// (memory) carries a mutex and an accessor for exactly that reason. Making the
// generator swappable the same way would mean editing every one of those reads
// in agent-go, releasing it, then releasing this — three releases to change a
// model name.
//
// `domain.Generator` is an interface, so there is a cheaper seam that is not a
// compromise: agent-go is handed THIS, forever, and what it delegates to is
// swapped underneath. The service never learns the provider changed, and it
// cannot race, because every call goes through the same lock.
//
// What this deliberately does not cover is the embedder. A knowledge index is
// built with one embedder at one dimension and can only be queried by that same
// embedder — swapping it does not reconfigure the index, it invalidates it. It
// is not a setting; it is a property of the data.
type SwappableLLM struct {
	mu   sync.RWMutex
	cur  agdomain.Generator
	desc LLMDesc
}

// LLMDesc is what the current generator was built from — the part that is safe
// to show an operator. The API key is deliberately absent: this is read by a
// status endpoint, and a struct that carries a secret eventually gets logged.
type LLMDesc struct {
	BaseURL string
	Model   string
}

// NewSwappableLLM wraps the generator built at startup.
func NewSwappableLLM(initial agdomain.Generator, desc LLMDesc) *SwappableLLM {
	return &SwappableLLM{cur: initial, desc: desc}
}

// Swap replaces the generator and returns the description it replaced.
func (s *SwappableLLM) Swap(next agdomain.Generator, desc LLMDesc) LLMDesc {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.desc
	s.cur, s.desc = next, desc
	return prev
}

// Desc reports what is currently generating.
func (s *SwappableLLM) Desc() LLMDesc {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.desc
}

// get is the single read point. A call that is already in flight keeps the
// generator it started with — Go's interface value is copied out under the
// lock, so a swap mid-request cannot leave a half-changed provider behind.
func (s *SwappableLLM) get() agdomain.Generator {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// ── domain.Generator ────────────────────────────────────────────────────

func (s *SwappableLLM) Generate(ctx context.Context, prompt string, opts *agdomain.GenerationOptions) (string, error) {
	return s.get().Generate(ctx, prompt, opts)
}

func (s *SwappableLLM) Stream(ctx context.Context, prompt string, opts *agdomain.GenerationOptions, cb func(string)) error {
	return s.get().Stream(ctx, prompt, opts, cb)
}

func (s *SwappableLLM) GenerateWithTools(ctx context.Context, msgs []agdomain.Message, tools []agdomain.ToolDefinition, opts *agdomain.GenerationOptions) (*agdomain.GenerationResult, error) {
	return s.get().GenerateWithTools(ctx, msgs, tools, opts)
}

func (s *SwappableLLM) StreamWithTools(ctx context.Context, msgs []agdomain.Message, tools []agdomain.ToolDefinition, opts *agdomain.GenerationOptions, cb agdomain.ToolCallCallback) error {
	return s.get().StreamWithTools(ctx, msgs, tools, opts, cb)
}

func (s *SwappableLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *agdomain.GenerationOptions) (*agdomain.StructuredResult, error) {
	return s.get().GenerateStructured(ctx, prompt, schema, opts)
}

func (s *SwappableLLM) RecognizeIntent(ctx context.Context, request string) (*agdomain.IntentResult, error) {
	return s.get().RecognizeIntent(ctx, request)
}

// NativeWebSearchVerdict forwards the one capability agent-go discovers by type
// assertion rather than by interface.
//
// Wrapping a provider hides it: `s.llmService.(domain.NativeWebSearchReporter)`
// would stop matching, and native web search would quietly turn off with no
// error anywhere. Implementing it here and delegating keeps the assertion true;
// returning (false, false) when the provider underneath cannot report is the
// same "no evidence either way" the caller already falls back to, so a provider
// that never supported this is described exactly as before.
func (s *SwappableLLM) NativeWebSearchVerdict() (supported, known bool) {
	if r, ok := s.get().(agdomain.NativeWebSearchReporter); ok {
		return r.NativeWebSearchVerdict()
	}
	return false, false
}

// Compile-time proof that the wrapper is substitutable for the thing it wraps.
// Without these, a method whose signature drifts from the interface turns into
// a silent fallback to the embedded nil at runtime.
var (
	_ agdomain.Generator               = (*SwappableLLM)(nil)
	_ agdomain.NativeWebSearchReporter = (*SwappableLLM)(nil)
)
