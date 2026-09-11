package agents

import (
	"context"
	"sync"
	"testing"

	agdomain "github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// fakeLLM answers with its own name, so a test can see which provider ran.
type fakeLLM struct{ name string }

func (f *fakeLLM) Generate(context.Context, string, *agdomain.GenerationOptions) (string, error) {
	return f.name, nil
}
func (f *fakeLLM) Stream(context.Context, string, *agdomain.GenerationOptions, func(string)) error {
	return nil
}
func (f *fakeLLM) GenerateWithTools(context.Context, []agdomain.Message, []agdomain.ToolDefinition, *agdomain.GenerationOptions) (*agdomain.GenerationResult, error) {
	return &agdomain.GenerationResult{Content: f.name}, nil
}
func (f *fakeLLM) StreamWithTools(context.Context, []agdomain.Message, []agdomain.ToolDefinition, *agdomain.GenerationOptions, agdomain.ToolCallCallback) error {
	return nil
}
func (f *fakeLLM) GenerateStructured(context.Context, string, interface{}, *agdomain.GenerationOptions) (*agdomain.StructuredResult, error) {
	return &agdomain.StructuredResult{}, nil
}
func (f *fakeLLM) RecognizeIntent(context.Context, string) (*agdomain.IntentResult, error) {
	return &agdomain.IntentResult{}, nil
}

// reportingLLM also answers the capability agent-go discovers by type assertion.
type reportingLLM struct {
	fakeLLM
	supported, known bool
}

func (r *reportingLLM) NativeWebSearchVerdict() (bool, bool) { return r.supported, r.known }

// The whole point: what agent-go holds never changes, and what it reaches
// changes underneath it.
func TestSwapChangesWhoAnswers(t *testing.T) {
	s := NewSwappableLLM(&fakeLLM{name: "first"}, LLMDesc{BaseURL: "https://a/v1", Model: "m1"})

	got, _ := s.Generate(context.Background(), "x", nil)
	if got != "first" {
		t.Fatalf("before swap = %q, want first", got)
	}

	prev := s.Swap(&fakeLLM{name: "second"}, LLMDesc{BaseURL: "https://b/v1", Model: "m2"})
	if prev.Model != "m1" || prev.BaseURL != "https://a/v1" {
		t.Errorf("Swap returned %+v as the previous settings, want m1 on https://a/v1", prev)
	}

	got, _ = s.Generate(context.Background(), "x", nil)
	if got != "second" {
		t.Errorf("after swap = %q, want second", got)
	}
	if d := s.Desc(); d.Model != "m2" || d.BaseURL != "https://b/v1" {
		t.Errorf("Desc = %+v, want m2 on https://b/v1", d)
	}
}

// The reason this type exists rather than a bare field. agent-go reads its
// generator from twenty-odd places with no lock, which is correct only while
// the field never changes; run this with -race.
func TestSwapWhileGenerating(t *testing.T) {
	s := NewSwappableLLM(&fakeLLM{name: "a"}, LLMDesc{Model: "a"})

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if _, err := s.Generate(context.Background(), "x", nil); err != nil {
						t.Error(err)
						return
					}
					_ = s.Desc()
					_, _ = s.NativeWebSearchVerdict()
				}
			}
		}()
	}
	for i := range 200 {
		name := "b"
		if i%2 == 0 {
			name = "c"
		}
		s.Swap(&fakeLLM{name: name}, LLMDesc{Model: name})
	}
	close(stop)
	wg.Wait()
}

// Wrapping a provider hides what agent-go finds by type assertion. Native web
// search would have turned off with no error anywhere — the caller reads
// `known` and simply believes it.
func TestWebSearchVerdictSurvivesTheWrapper(t *testing.T) {
	s := NewSwappableLLM(&reportingLLM{supported: true, known: true}, LLMDesc{})
	if supported, known := s.NativeWebSearchVerdict(); !supported || !known {
		t.Errorf("verdict = (%v, %v) through the wrapper, want (true, true) — the provider reports it", supported, known)
	}

	// And a provider that cannot report gives the same "no evidence" the caller
	// already falls back to, rather than a confident false.
	s.Swap(&fakeLLM{}, LLMDesc{})
	if supported, known := s.NativeWebSearchVerdict(); supported || known {
		t.Errorf("verdict = (%v, %v) for a provider that cannot report, want (false, false)", supported, known)
	}
}

// The wrapper is handed to agent-go in place of a provider, so it has to be one.
func TestWrapperIsAGenerator(t *testing.T) {
	var _ agdomain.Generator = NewSwappableLLM(&fakeLLM{}, LLMDesc{})
	var _ agdomain.NativeWebSearchReporter = NewSwappableLLM(&fakeLLM{}, LLMDesc{})
}
