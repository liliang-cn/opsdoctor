# Onboarding a new project → a deployed agent

opsdoctor is product-agnostic: the engine never changes. Bringing a new OSS
project online is a repeatable pipeline whose only product-specific input is a
`domain.toml`. This is the standardized process.

```
write domain.toml  →  build knowledge base  →  serve  →  deploy
   (the only            (ingest-repo ×N,        (single      (make deploy +
    judgment)            import-schema)          binary)       Caddy/systemd)
```

The engine has no compiled-in product knowledge. A worked example
(`examples/example/domain.toml`) ships as a template; copy it and edit.

---

## 0. Prerequisites

- Go 1.25+ (build). Node only if you change the front-end (`make web`).
- An **embedder** and an **LLM**, both OpenAI-compatible. We use Alibaba DashScope:
  - LLM: `qwen3.7-plus`
  - Embedder: `text-embedding-v4` (1024-dim)
  - (any OpenAI-compatible endpoint works; the query embedder MUST match the one
    used to build the index.)
- For code-graph ingestion: the [Understand-Anything](https://github.com/Egonex-AI/Understand-Anything)
  `/understand` skill reachable via `OPSDOCTOR_UNDERSTAND_CMD` (e.g. `claude -p "/understand ." --dangerously-skip-permissions`).

Environment used throughout (export once):

```bash
export OPSDOCTOR_DOMAIN_FILE=examples/example/domain.toml
export OPSDOCTOR_LLM_API_KEY=<key>   OPSDOCTOR_LLM_BASE_URL=https://dashscope.aliyuncs.com/compatible-mode/v1  OPSDOCTOR_LLM_MODEL=qwen3.7-plus
export OPSDOCTOR_EMB_API_KEY=<key>   OPSDOCTOR_EMB_BASE_URL=https://dashscope.aliyuncs.com/compatible-mode/v1  OPSDOCTOR_EMB_MODEL=text-embedding-v4  OPSDOCTOR_EMB_DIM=1024
export OPSDOCTOR_UNDERSTAND_CMD='claude -p "/understand ." --dangerously-skip-permissions'
```

---

## 1. Write `domain.toml` (the only product-specific input)

**Fast start — let the LLM draft it:**

```bash
opsdoctor init https://github.com/<org>/<repo>   # clone + LLM → domain.generated.toml
```

`init` inspects the repo (README, top-level layout, dominant languages) and drafts
`name`/`title`/`persona`, the `entity_types`/`relation_types` vocabulary,
`error_patterns`, candidate `probes`, and candidate `red_lines`. **You MUST review
the draft** — especially `[[red_lines]]` (the destructive-command safety wall) and
`[[probes]]` (commands the agent may run) — then `cp domain.generated.toml domain.toml`.

**Or by hand:** copy `examples/example/domain.toml` and edit. Fields:

| field | meaning |
|---|---|
| `name` / `title` | engine label / UI brand |
| `persona` | the agent's system prompt (who it is, how to answer, safety posture) |
| `entity_types` / `relation_types` | the domain ontology vocabulary. Declare it: `relation_types` is both what extraction may emit and what retrieval traverses, so a domain without one gets a graph whose edges nothing follows |
| `error_patterns` | regexes (group 1 = message) for log triage |
| `[[probes]]` | read-only diagnostic commands the agent may run |
| `repos` | upstream repos to ingest |
| `[[red_lines]]` | destructive-command blocks (the deterministic safety wall) |

`opsdoctor domain` prints the loaded config to verify the TOML.

---

## 2. Build the knowledge base

Two layers get built into `data/knowledge.db`: a **graph** (code structure +
object model) and **semantic vectors** (for retrieval).

**a) Code repos → graph** (one command per repo, fully automatic):

```bash
opsdoctor ingest-repo https://github.com/<org>/<repo>      # clone → /understand → import-graph
```

If `/understand` stalls on a huge repo (no final `knowledge-graph.json`),
`ingest-repo` now **auto-salvages**: it merges the intermediate batches
(`assembled-graph.json` + `batch-*.json` + layers) into an equivalent graph and
imports that. To redo it explicitly on an existing clone:

```bash
opsdoctor salvage repos/<repo>     # rebuild + import from intermediate batches, no re-run
```

**b) Docs / KB / blog → semantic vectors** (point at a dir of .md/.adoc):

```bash
opsdoctor ingest-repo repos/<docs-dir>     # no knowledge-graph.json → text/semantic ingest
```

**c) Object model → graph** (deterministic, no LLM). Use whatever structured
source the project ships — this is the precise ontology, don't mine it from prose:

```bash
opsdoctor import-model path/to/source     # auto-detects format → entities + REFERENCES
```

> Adapters (pick the one matching the project's source-of-truth):
> | source | extension | maps to |
> |---|---|---|
> | SQL DDL | `.sql` | `CREATE TABLE` → entity, `FOREIGN KEY` → relation |
> | Protobuf | `.proto` | `message` → entity, message-typed field → relation |
> | OpenAPI / Swagger | `.yaml` `.yml` `.json` | `components.schemas` → entity, `$ref` → relation |
> | C/C++ struct | `.h` `.hpp` `.c` `.cc` `.cpp` | `struct` → entity, struct-typed field → relation |
>
> `import-schema` remains as the SQL-only alias; `import-model` covers all of the
> above (and routes `.sql` to the same parser). For a project with no SQL — e.g.
> a C codebase — point `import-model` at its core header(s) or `.proto`.

**d) Verify:**

```bash
opsdoctor search "<concept>"          # vector + keyword
opsdoctor search-graph "<concept>"    # + one-hop graph expansion
opsdoctor ask "<question>"            # full agent (probes + knowledge_search + red-line wall)
```

**Updating a source later** (catches deletions, no full rebuild):

```bash
opsdoctor refresh <repo-or-dir>
```

---

## 3. Run locally

```bash
make run            # = go build + ./opsdoctor serve   (API under /api/*, UI at /)
# or:  opsdoctor ui   (also opens the browser)
```

Open `http://localhost:7634`. Without an LLM key it serves search-only.

---

## 4. Deploy (single binary + systemd + Caddy)

The binary embeds the web UI, and with cloud LLM/embedder there are **no runtime
deps** (no ollama). First-time provisioning is below; updates are `make deploy`.

### 4.1 First-time provision (on the server, once)

```bash
ssh HOST 'mkdir -p /opt/opsdoctor/data'
scp domain.toml HOST:/opt/opsdoctor/domain.toml
scp data/knowledge.db HOST:/opt/opsdoctor/data/knowledge.db   # ship the prebuilt index

# env (root-only)
ssh HOST 'cat > /opt/opsdoctor/opsdoctor.env <<ENV
OPSDOCTOR_DOMAIN_FILE=/opt/opsdoctor/domain.toml
OPSDOCTOR_KNOWLEDGE_DB_PATH=/opt/opsdoctor/data/knowledge.db
OPSDOCTOR_DB_PATH=/opt/opsdoctor/data/opsdoctor.db
OPSDOCTOR_HTTP_ADDR=127.0.0.1:47634
OPSDOCTOR_LLM_API_KEY=...      OPSDOCTOR_LLM_BASE_URL=...  OPSDOCTOR_LLM_MODEL=qwen3.7-plus
OPSDOCTOR_EMB_API_KEY=...      OPSDOCTOR_EMB_BASE_URL=...  OPSDOCTOR_EMB_MODEL=text-embedding-v4  OPSDOCTOR_EMB_DIM=1024
OPSDOCTOR_RATE_LIMIT_PER_MIN=30
ENV
chmod 600 /opt/opsdoctor/opsdoctor.env'
```

systemd unit `/etc/systemd/system/opsdoctor.service`:

```ini
[Unit]
Description=opsdoctor
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
WorkingDirectory=/opt/opsdoctor
EnvironmentFile=/opt/opsdoctor/opsdoctor.env
ExecStart=/opt/opsdoctor/opsdoctor serve
Restart=on-failure
RestartSec=3
[Install]
WantedBy=multi-user.target
```

```bash
ssh HOST 'systemctl daemon-reload && systemctl enable --now opsdoctor'
```

### 4.1a A host provisioned as oss-agent

The program was called oss-agent until v0.38.0. A host set up under that name
keeps working as it is: the binary reads every `OPSDOCTOR_*` variable and falls
back to the `OSS_*` name, so the old env file needs no edit. Only the deploy
target moved — `make deploy` now ships to `/opt/opsdoctor` and restarts the
`opsdoctor` unit. Either re-provision under the new name (above), or keep the
old layout with `make deploy REMOTE_DIR=/opt/oss-agent BIN=oss-agent`.

### 4.2 Caddy: TLS + basic auth + reverse proxy

The public API has no built-in auth; gate the whole origin at the proxy (a token
baked into the public UI JS would be readable by anyone, so origin-level basic
auth is the right control). Generate a hash and add a site block:

```bash
ssh HOST 'caddy hash-password --plaintext "<password>"'   # → $2a$14$...
```

```caddy
oss.example.com {
    basic_auth {
        <user> <bcrypt-hash>
    }
    reverse_proxy 127.0.0.1:47634 {
        flush_interval -1          # required for streaming chat / SSE
    }
}
```

```bash
ssh HOST 'caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile && systemctl reload caddy'
```

Point DNS `oss.example.com A → <server ip>`; Caddy auto-provisions the cert.

### 4.3 Rate limiting

Built into the app: per-IP sliding-window cap on the LLM endpoints
(`/ask`, `/ask/stream`, `/diagnose`, `/chat`, `/chat/stream`, `/analyze-log`),
returns 429 when exceeded. Configure with `OPSDOCTOR_RATE_LIMIT_PER_MIN` (default 30,
`0` = unlimited). Read-only/static endpoints are not limited.

### 4.4 Updates

```bash
make deploy HOST=<host>     # cross-compile linux/amd64 → scp → restart service
make push-db HOST=<host>    # ship a freshly rebuilt knowledge.db
```

---

## Environment reference

| var | default | purpose |
|---|---|---|
| `OPSDOCTOR_DOMAIN_FILE` | `./domain.toml` | active product config |
| `OPSDOCTOR_LLM_API_KEY` / `_BASE_URL` / `_MODEL` | — / OpenAI / `gpt-4o` | reasoning LLM |
| `OPSDOCTOR_EMB_API_KEY` / `_BASE_URL` / `_MODEL` / `_DIM` | (LLM creds) / `text-embedding-3-small` / 1536 | embedder (must match the index) |
| `OPSDOCTOR_KNOWLEDGE_DB_PATH` | `./data/knowledge.db` | cortexdb knowledge base |
| `OPSDOCTOR_DB_PATH` | `./data/opsdoctor.db` | agent-go session store |
| `OPSDOCTOR_HTTP_ADDR` | `:7634` | serve listen address |
| `OPSDOCTOR_CONV_MEMORY` | `on` | cross-session chat memory |
| `OPSDOCTOR_RATE_LIMIT_PER_MIN` | `30` | per-IP LLM-endpoint cap (0 = off) |
| `OPSDOCTOR_UNDERSTAND_CMD` | — | command to produce knowledge-graph.json |

---

## What's standardized vs per-project

- **Fully automatic, any project**: code→graph (`ingest-repo`, auto-salvages a
  stalled understand run), docs→vectors, `serve`, the agent itself
  (domain.toml-driven), deploy (`make deploy`).
- **Finite adapters, pick one**: object-model extraction from a structured source
  (`import-model`: SQL / proto / OpenAPI / C-struct).
- **LLM-assisted, human-reviewed**: drafting `domain.toml` (`opsdoctor init`).
- **Irreducible human input**: reviewing/owning the `domain.toml` — above all the
  `red_lines` safety wall — and choosing which structured source is the object
  model. Everything else is the pipeline above.
