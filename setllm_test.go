package opsdoctor

import (
	"testing"

	"github.com/liliang-cn/opsdoctor/internal/agents"
)

// bare builds just enough Agent to exercise SetLLM: the swap handle and the key
// in force. Everything else SetLLM does not touch.
func bare(baseURL, model, key string) *Agent {
	return &Agent{
		llm:       agents.NewSwappableLLM(nil, agents.LLMDesc{BaseURL: baseURL, Model: model}),
		llmAPIKey: key,
	}
}

// An empty field keeps what is running. This is what makes "switch the model,
// keep the gateway" a one-field edit — and, more to the point, what stops a
// settings form from demanding the API key back every time somebody renames a
// model.
func TestSetLLMKeepsWhatWasNotSent(t *testing.T) {
	a := bare("https://cpa.example/v1", "gemini-3.8-flash-high", "sk-original")

	prev, err := a.SetLLM(LLMSettings{Model: "deepseek-v4-flash"}, "")
	if err != nil {
		t.Fatalf("SetLLM: %v", err)
	}
	if prev.Model != "gemini-3.8-flash-high" {
		t.Errorf("previous model = %q, want the one that was running", prev.Model)
	}

	got := a.LLM()
	if got.Model != "deepseek-v4-flash" {
		t.Errorf("model = %q, want the new one", got.Model)
	}
	if got.BaseURL != "https://cpa.example/v1" {
		t.Errorf("base URL = %q; an unsent field must keep its value", got.BaseURL)
	}
	if a.llmAPIKey != "sk-original" {
		t.Error("the API key was cleared by an edit that never mentioned it")
	}
}

func TestSetLLMReplacesTheKeyWhenOneIsSent(t *testing.T) {
	a := bare("https://cpa.example/v1", "m", "sk-old")
	if _, err := a.SetLLM(LLMSettings{}, "sk-new"); err != nil {
		t.Fatalf("SetLLM: %v", err)
	}
	if a.llmAPIKey != "sk-new" {
		t.Errorf("key = %q, want sk-new", a.llmAPIKey)
	}
}

// An agent with no model name cannot generate, and the failure would arrive on
// the next question rather than here.
func TestSetLLMRefusesAnEmptyModel(t *testing.T) {
	a := bare("https://cpa.example/v1", "", "sk")
	if _, err := a.SetLLM(LLMSettings{BaseURL: "https://other/v1"}, ""); err == nil {
		t.Fatal("a swap that leaves no model name succeeded")
	}
	if got := a.LLM(); got.BaseURL != "https://cpa.example/v1" {
		t.Errorf("a refused swap changed the base URL to %q", got.BaseURL)
	}
}

// The API key never leaves through the struct an operator reads. A settings
// endpoint returns this, and a field that carries a secret is a secret in a log.
func TestLLMSettingsCarryNoSecret(t *testing.T) {
	a := bare("https://cpa.example/v1", "m", "sk-do-not-leak")
	for _, v := range []string{a.LLM().BaseURL, a.LLM().Model} {
		if v == "sk-do-not-leak" {
			t.Fatal("the API key surfaced in LLMSettings")
		}
	}
}
