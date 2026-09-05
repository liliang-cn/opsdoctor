# opsdoctor

A **product-agnostic platform** for building AI ops & support agents over an
open-source project. The engine knows nothing about any specific product — a
"domain" is supplied entirely by a `domain.toml`. A worked example domain ships
under `examples/`; the same engine serves any storage/infra project.

## What it does

- **Builds a knowledge graph** of a project's source, docs, and internal know-how
  by importing [Understand-Anything](https://github.com/Egonex-AI/Understand-Anything)
  `knowledge-graph.json` (AST + LLM ontology) and/or semantically ingesting prose
  docs/skills — all stored in [cortexdb](https://github.com/liliang-cn/cortexdb)
  (vectors + graph).
- **Answers & diagnoses** via an [agent-go](https://github.com/liliang-cn/agent-go)
  `Agent` (ReAct loop): it calls read-only **probes** and a GraphRAG
  **knowledge_search** tool, grounds answers in the real code/docs, and is guarded
  by a deterministic **red-line wall** that blocks destructive one-liners.
- **Triages logs** (`analyze-log`): any file/dir/archive → de-duplicated, ranked
  findings (generic severities + the domain's `error_patterns`) → optional
  AI root-cause grounded in the knowledge graph.

## Architecture

```
domain.toml ──┐                         ┌── probes (read-only diagnostics)
              ├── engine (product-free) ─┼── knowledge_search (GraphRAG / cortexdb)
repos/docs ───┘                         └── red-line safety wall (deterministic)
                          │
        agent-go Agent (ReAct) ── LLM (OpenAI-compatible) + embedder (e.g. ollama)
```

- `internal/domain`  — loads `domain.toml` (persona, entity/relation types,
  error_patterns, probes, repos, red_lines). The only place a "product" enters.
- `internal/knowledge` — cortexdb wrapper (embed, semantic search, graph upsert).
- `internal/graphimport` — imports Understand-Anything `knowledge-graph.json`.
- `internal/ingest` — semantic text ingest for prose docs/skills.
- `internal/loganalyze` — product-agnostic log triage engine.
- `internal/agents` — wires the agent-go Agent (tools + lint). Plain
  function-calling, so a question gets one clean answer.
- `internal/safety` — the red-line filter (regex, severity → unlock-key gate).
- `internal/extract` — ingest-time LLM ontology extractor (raw LLM, no agent loop).

## Commands

```
opsdoctor ask <question>       one-shot Q&A (ReAct: probes + knowledge_search)
opsdoctor diagnose <symptom>   same loop, framed for troubleshooting
opsdoctor chat                 multi-turn (history kept across turns)
opsdoctor serve                start the HTTP API (OPSDOCTOR_HTTP_ADDR, default :7634)
opsdoctor analyze-log <path>   triage a log file / dir / .tar.gz / .zip, + AI diagnosis
opsdoctor ingest <dir>         ingest *.md docs
opsdoctor ingest-repo <url>    clone → understand → import (graph), or text fallback
opsdoctor refresh <url|dir>    purge one source (catches deletions) and re-import it
opsdoctor import-graph <f>     import an Understand-Anything knowledge-graph.json
opsdoctor search <query>       query the knowledge base directly (no LLM)
opsdoctor search-graph <query> search + one-hop graph expansion (calls/contains/…)
opsdoctor check <command...>   test a command against the red-line wall
opsdoctor domain               print the loaded domain config
```

## HTTP API (`opsdoctor serve`)

A thin JSON layer over the same agent + knowledge store. Address via `OPSDOCTOR_HTTP_ADDR`
(default `:7634`). Without an LLM key it serves the search endpoints only.

```
GET  /healthz                     status + loaded domain
POST /ask            {question}                  → {answer}        (one-shot)
POST /chat           {session_id?, message}      → {session_id, answer}  (multi-turn; history persisted)
POST /ask/stream     {question} or ?q=           → SSE token stream (falls back to one chunk)
POST /diagnose       {question}                  → {answer}        (troubleshooting framing)
POST /search         {query, top_k}              → {hits}
POST /search-graph   {query, top_k}              → {hits, related_via_graph}
POST /analyze-log    multipart 'log' file OR {path}   → triage groups; ?diagnose=true adds AI root-cause
```

Multi-turn history is keyed by `session_id` and persisted by agent-go's session
store (at `OPSDOCTOR_DB_PATH`), so conversations survive across requests and restarts.

Two memory layers on `/chat`:
- **within-session** — agent-go session history (same `session_id`).
- **cross-session** — every turn is also embedded into cortexdb's memory
  (global `conversations` bucket); each new question semantically recalls relevant
  turns from *any* past conversation and prepends them as optional context. The
  response's `recalled` field reports how many were pulled. Toggle with
  `OPSDOCTOR_CONV_MEMORY=off`.

## Configuration (env)

```
OPSDOCTOR_DOMAIN_FILE     path to the active domain.toml (e.g. examples/example/domain.toml)
OPSDOCTOR_LLM_API_KEY     LLM key (OpenAI-compatible)   OPSDOCTOR_LLM_BASE_URL / OPSDOCTOR_LLM_MODEL
OPSDOCTOR_EMB_API_KEY     embedder key                  OPSDOCTOR_EMB_BASE_URL / OPSDOCTOR_EMB_MODEL / OPSDOCTOR_EMB_DIM
OPSDOCTOR_KNOWLEDGE_DB_PATH  cortexdb path (default ./data/knowledge.db)
OPSDOCTOR_HTTP_ADDR       HTTP API listen address for `serve` (default :7634)
OPSDOCTOR_CONV_MEMORY     cross-session chat memory on /chat (default on; set "off" to disable)
OPSDOCTOR_UNDERSTAND_CMD  command run in a repo to produce knowledge-graph.json
```

The embedder used to query must match the one used to build the store (e.g. ollama
`embeddinggemma`, 768-dim).

## Building a new domain

1. Write a `domain.toml` (see `examples/example/domain.toml`): persona,
   entity/relation types, `error_patterns`, `probes`, `repos`, `red_lines`.
2. Point `OPSDOCTOR_DOMAIN_FILE` at it.
3. Ingest the project's repos (`ingest-repo`) and docs (`ingest-repo` text path).
4. `ask` / `analyze-log` away. No engine code changes.

## Use as a library

The CLI and HTTP server are one app built on the public package
`github.com/liliang-cn/opsdoctor` — embed the same engine in your own program:

```go
import opsdoctor "github.com/liliang-cn/opsdoctor"

// Zero-value fields fall back to OPSDOCTOR_* env vars, then defaults.
a, err := opsdoctor.New(opsdoctor.Config{DomainFile: "domain.toml"})
if err != nil { log.Fatal(err) }
defer a.Close()

answer, _ := a.Ask(ctx, "How do I recover a StandAlone resource?")

// Stream tool calls + the grounded, cited answer:
a.Stream(ctx, question, func(e opsdoctor.Event) {
    switch e.Kind {
    case opsdoctor.EventToolCall: log.Printf("tool %s %v", e.Tool, e.Args)
    case opsdoctor.EventText:     fmt.Print(e.Text)
    }
})
```

| method | what it does |
|---|---|
| `New(Config)` / `Close()` | build / release the agent (LLM + knowledge store) |
| `Ask` / `Diagnose` | one-shot grounded answer (ReAct: probes + knowledge_search + red-line wall) |
| `Chat(sessionID, msg)` | multi-turn with persisted per-session history |
| `Stream(q, on)` | live tool calls + answer deltas; returns the full answer + cited sources |
| `Search(q, k)` | top reranked knowledge chunks (no LLM) |
| `CheckCommand(cmd)` | run a command through the deterministic red-line wall |
| `AnalyzeLog(path)` | triage a log file / dir / archive into ranked problems |
| `IngestDoc` / `IngestDir` | add docs to the knowledge base (embedded with the configured embedder) |
| `Refresh(dir)` / `PurgeSource` | re-ingest a source (catches updates + deletions) / remove one |
| `MCPStatus()` | per-server outcome of mounting `Config.MCPServers` (read-only tools into ReAct) |

Updates re-embed with the Agent's embedder, so it **must** match the target DB's
model/dimension (e.g. the extracted `drbd-reactor.db` is `text-embedding-v4` / 1024-dim)
— mismatched vectors corrupt retrieval.

Config is env-first, so a process configured via `OPSDOCTOR_*` can call
`opsdoctor.New(opsdoctor.Config{})`. A runnable example lives in `examples/lib/`.
