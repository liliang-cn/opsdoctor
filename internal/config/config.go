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
	// SharedKnowledgeDBPaths are read-only bases searched alongside the
	// knowledge base: knowledge that is the same everywhere the product runs,
	// built once and installed as a file. Comma-separated in the environment.
	SharedKnowledgeDBPaths []string

	// Path to the active domain config (domain.toml). Each project supplies its own.
	DomainFile string

	// alchemy, the extraction service. Empty AlchemyAddr means prose is read
	// by the built-in per-chunk LLM extractor instead. See internal/alchemyclient.
	AlchemyAddr  string
	AlchemyToken string
	AlchemyTLS   bool
}

// Load reads config from environment variables with sensible defaults.
//
//	STEWARD_LLM_BASE_URL / STEWARD_LLM_API_KEY / STEWARD_LLM_MODEL
//	STEWARD_EMB_BASE_URL / STEWARD_EMB_API_KEY / STEWARD_EMB_MODEL
//	STEWARD_DB_PATH
//	STEWARD_ALCHEMY_ADDR / STEWARD_ALCHEMY_TOKEN / STEWARD_ALCHEMY_TLS
func Load() Config {
	llmBase := env("STEWARD_LLM_BASE_URL", "https://api.openai.com/v1")
	llmKey := Getenv("STEWARD_LLM_API_KEY")
	return Config{
		LLMBaseURL:             llmBase,
		LLMAPIKey:              llmKey,
		LLMModel:               env("STEWARD_LLM_MODEL", "gpt-4o"),
		EmbBaseURL:             env("STEWARD_EMB_BASE_URL", llmBase),
		EmbAPIKey:              env("STEWARD_EMB_API_KEY", llmKey),
		EmbModel:               env("STEWARD_EMB_MODEL", "text-embedding-3-small"),
		DBPath:                 env("STEWARD_DB_PATH", "./data/steward.db"),
		KnowledgeDBPath:        env("STEWARD_KNOWLEDGE_DB_PATH", "./data/knowledge.db"),
		EmbDim:                 envInt("STEWARD_EMB_DIM", 1536),
		SharedKnowledgeDBPaths: splitList(Getenv("STEWARD_SHARED_KNOWLEDGE_DBS")),
		DomainFile:             env("STEWARD_DOMAIN_FILE", "./domain.toml"),
		AlchemyAddr:            Getenv("STEWARD_ALCHEMY_ADDR"),
		AlchemyToken:           Getenv("STEWARD_ALCHEMY_TOKEN"),
		AlchemyTLS:             envBool("STEWARD_ALCHEMY_TLS"),
	}
}

// legacyPrefixes are what every variable was called before the program was
// renamed: OPSPILOT_* until v0.51.0, OPSDOCTOR_* until v0.50.0, OSS_*
// (oss-agent) until v0.38.0. Newest first, because that is the order a host
// that has been re-provisioned more than once should be read in. A host
// provisioned under an old name has an EnvironmentFile full of those names,
// and a rename that silently read none of them would start the service with no
// key, no domain and an empty knowledge base — the same shape as every other
// misconfiguration, with nothing in the log naming the cause.
var legacyPrefixes = []string{"OPSPILOT_", "OPSDOCTOR_", "OSS_"}

// Getenv reads a STEWARD_* variable, falling back to its OPSPILOT_*, then
// OPSDOCTOR_*, then OSS_* name. The newest name wins when several are set.
func Getenv(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if strings.HasPrefix(key, "STEWARD_") {
		for _, p := range legacyPrefixes {
			if v := os.Getenv(p + strings.TrimPrefix(key, "STEWARD_")); v != "" {
				return v
			}
		}
	}
	return ""
}

// envBool is true for 1/true/yes/on, case-insensitively, and false for
// anything else including unset.
func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
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

// splitList splits a comma-separated setting, dropping blanks.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
