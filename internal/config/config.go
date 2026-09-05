// Package config loads runtime configuration from the environment.
// Cloud frontier models only (OpenAI-compatible API). No local models.
package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	// LLM (reasoning / orchestration brain)
	LLMBaseURL string
	LLMAPIKey  string
	LLMModel   string

	// Embedder (for cortexdb GraphRAG memory)
	EmbBaseURL string
	EmbAPIKey  string
	EmbModel   string

	// On-disk store for the agent's graph memory (cortexdb).
	DBPath string

	// Knowledge base (GraphRAG over the active domain's material).
	KnowledgeDBPath string
	EmbDim          int

	// Path to the active domain config (domain.toml). Each project supplies its own.
	DomainFile string
}

// Load reads config from environment variables with sensible defaults.
//
//	OPSDOCTOR_LLM_BASE_URL / OPSDOCTOR_LLM_API_KEY / OPSDOCTOR_LLM_MODEL
//	OPSDOCTOR_EMB_BASE_URL / OPSDOCTOR_EMB_API_KEY / OPSDOCTOR_EMB_MODEL
//	OPSDOCTOR_DB_PATH
func Load() Config {
	llmBase := env("OPSDOCTOR_LLM_BASE_URL", "https://api.openai.com/v1")
	llmKey := Getenv("OPSDOCTOR_LLM_API_KEY")
	return Config{
		LLMBaseURL:      llmBase,
		LLMAPIKey:       llmKey,
		LLMModel:        env("OPSDOCTOR_LLM_MODEL", "gpt-4o"),
		EmbBaseURL:      env("OPSDOCTOR_EMB_BASE_URL", llmBase),
		EmbAPIKey:       env("OPSDOCTOR_EMB_API_KEY", llmKey),
		EmbModel:        env("OPSDOCTOR_EMB_MODEL", "text-embedding-3-small"),
		DBPath:          env("OPSDOCTOR_DB_PATH", "./data/opsdoctor.db"),
		KnowledgeDBPath: env("OPSDOCTOR_KNOWLEDGE_DB_PATH", "./data/knowledge.db"),
		EmbDim:          envInt("OPSDOCTOR_EMB_DIM", 1536),
		DomainFile:      env("OPSDOCTOR_DOMAIN_FILE", "./domain.toml"),
	}
}

// legacyPrefix is what every variable was called before the program was
// renamed. A host provisioned as oss-agent has an EnvironmentFile full of
// OSS_* names, and a rename that silently read none of them would start the
// service with no key, no domain and an empty knowledge base — the same shape
// as every other misconfiguration, with nothing in the log naming the cause.
const legacyPrefix = "OSS_"

// Getenv reads an OPSDOCTOR_* variable, falling back to the OSS_* name it had
// before v0.38.0. The new name wins when both are set.
func Getenv(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if strings.HasPrefix(key, "OPSDOCTOR_") {
		return os.Getenv(legacyPrefix + strings.TrimPrefix(key, "OPSDOCTOR_"))
	}
	return ""
}

func envInt(key string, def int) int {
	if v := Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func env(key, def string) string {
	if v := Getenv(key); v != "" {
		return v
	}
	return def
}
