# Onboarding a new project → a deployed agent

steward is product-agnostic: the engine never changes. Bringing a new OSS
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
- An **embedder** and an **LLM**, both OpenAI-compatible. They need not come from
  the same provider — here the LLM is on the cpa gateway and the embedder is
  Alibaba DashScope:
  - LLM: `gemini-3.8-flash-high` on `https://cpa.superleo.app/v1`
  - Embedder: `text-embedding-v4` (1024-dim) on DashScope
  - (any OpenAI-compatible endpoint works; the query embedder MUST match the one
    used to build the index — the LLM can be swapped at any time, the embedder
    cannot.)
- For code-graph ingestion: the [Understand-Anything](https://github.com/Egonex-AI/Understand-Anything)
  `/understand` skill reachable via `STEWARD_UNDERSTAND_CMD` (e.g. `claude -p "/understand ." --dangerously-skip-permissions`).

Environment used throughout (export once):

```bash
export STEWARD_DOMAIN_FILE=examples/example/domain.toml
export STEWARD_LLM_API_KEY=<key>   STEWARD_LLM_BASE_URL=https://cpa.superleo.app/v1  STEWARD_LLM_MODEL=gemini-3.8-flash-high
export STEWARD_EMB_API_KEY=<key>   STEWARD_EMB_BASE_URL=https://dashscope.aliyuncs.com/compatible-mode/v1  STEWARD_EMB_MODEL=text-embedding-v4  STEWARD_EMB_DIM=1024
export STEWARD_UNDERSTAND_CMD='claude -p "/understand ." --dangerously-skip-permissions'
```

---

## 1. Write `domain.toml` (the only product-specific input)

**Fast start — let the LLM draft it:**

```bash
steward init https://github.com/<org>/<repo>   # clone + LLM → domain.generated.toml
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

`steward domain` prints the loaded config to verify the TOML.

---

## 2. Build the knowledge base

Two layers get built into `data/knowledge.db`: a **graph** (code structure +
object model) and **semantic vectors** (for retrieval).

**a) Code repos → graph** (one command per repo, fully automatic):

```bash
steward ingest-repo https://github.com/<org>/<repo>      # clone → /understand → import-graph
```

If `/understand` stalls on a huge repo (no final `knowledge-graph.json`),
`ingest-repo` now **auto-salvages**: it merges the intermediate batches
(`assembled-graph.json` + `batch-*.json` + layers) into an equivalent graph and
imports that. To redo it explicitly on an existing clone:

```bash
steward salvage repos/<repo>     # rebuild + import from intermediate batches, no re-run
```

**b) Docs / KB / blog → semantic vectors** (point at a dir of .md/.adoc):

```bash
steward ingest-repo repos/<docs-dir>     # no knowledge-graph.json → text/semantic ingest
```

**c) Object model → graph** (deterministic, no LLM). Use whatever structured
source the project ships — this is the precise ontology, don't mine it from prose:

```bash
steward import-model path/to/source     # auto-detects format → entities + REFERENCES
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
steward search "<concept>"          # vector + keyword
steward search-graph "<concept>"    # + one-hop graph expansion
steward ask "<question>"            # full agent (probes + knowledge_search + red-line wall)
```

**Updating a source later** (catches deletions, no full rebuild):

```bash
steward refresh <repo-or-dir>
```

---

### 2.x Extract through alchemy (optional)

Point `STEWARD_ALCHEMY_ADDR` (and `_TOKEN`) at an alchemy service and every
`ingest` / `ingest-repo` / `refresh` reads prose through it: one job per
document, the domain's vocabulary translated into alchemy's ontology, and the
result written with provenance on every node and edge. A document alchemy holds
for review is logged with the job id and the conflict's subject, keeps its
vectors, and gets its graph on the next `refresh` after the review is answered.
The LLM alchemy extracts with is the one in `STEWARD_LLM_*`; no embedder is
sent, the store embeds its own chunks. `steward doctor` has an `alchemy` line.

## 3. Run locally

```bash
make run            # = go build + ./steward serve   (API under /api/*, UI at /)
# or:  steward ui   (also opens the browser)
```

Open `http://localhost:7634`. Without an LLM key it serves search-only.

---

## 4. Deploy (single binary + systemd + Caddy)

The binary embeds the web UI, and with cloud LLM/embedder there are **no runtime
deps** (no ollama). First-time provisioning is below; updates are `make deploy`.

### 4.1 First-time provision (on the server, once)

```bash
ssh HOST 'mkdir -p /opt/steward/data'
scp domain.toml HOST:/opt/steward/domain.toml
scp data/knowledge.db HOST:/opt/steward/data/knowledge.db   # ship the prebuilt index

# env (root-only)
ssh HOST 'cat > /opt/steward/steward.env <<ENV
STEWARD_DOMAIN_FILE=/opt/steward/domain.toml
STEWARD_KNOWLEDGE_DB_PATH=/opt/steward/data/knowledge.db
STEWARD_DB_PATH=/opt/steward/data/steward.db
STEWARD_HTTP_ADDR=127.0.0.1:47634
STEWARD_LLM_API_KEY=...      STEWARD_LLM_BASE_URL=https://cpa.superleo.app/v1  STEWARD_LLM_MODEL=gemini-3.8-flash-high
STEWARD_EMB_API_KEY=...      STEWARD_EMB_BASE_URL=...  STEWARD_EMB_MODEL=text-embedding-v4  STEWARD_EMB_DIM=1024
STEWARD_RATE_LIMIT_PER_MIN=30
ENV
chmod 600 /opt/steward/steward.env'
```

systemd unit `/etc/systemd/system/steward.service`:

```ini
[Unit]
Description=steward
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
WorkingDirectory=/opt/steward
EnvironmentFile=/opt/steward/steward.env
ExecStart=/opt/steward/steward serve
Restart=on-failure
RestartSec=3
[Install]
WantedBy=multi-user.target
```

```bash
ssh HOST 'systemctl daemon-reload && systemctl enable --now steward'
```

### 4.1a A host provisioned under an old name

The program was called opspilot until v0.51.0, opsdoctor until v0.50.0 and
oss-agent until v0.38.0. A host set up under any of those names keeps working as
it is: the binary reads every `STEWARD_*` variable and falls back to the
`OPSPILOT_*`, then `OPSDOCTOR_*`, then `OSS_*` name, so the old env file needs no
edit. Only the deploy
target moved — `make deploy` now ships to `/opt/steward` and restarts the
`steward` unit. Either re-provision under the new name (above), or keep the
old layout with `make deploy REMOTE_DIR=/opt/<old-name> BIN=<old-name>` (e.g.
`opspilot`, `opsdoctor` or `oss-agent`).

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
returns 429 when exceeded. Configure with `STEWARD_RATE_LIMIT_PER_MIN` (default 30,
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
| `STEWARD_DOMAIN_FILE` | `./domain.toml` | active product config |
| `STEWARD_LLM_API_KEY` / `_BASE_URL` / `_MODEL` | — / OpenAI / `gpt-4o` | reasoning LLM |
| `STEWARD_EMB_API_KEY` / `_BASE_URL` / `_MODEL` / `_DIM` | (LLM creds) / `text-embedding-3-small` / 1536 | embedder (must match the index) |
| `STEWARD_KNOWLEDGE_DB_PATH` | `./data/knowledge.db` | cortexdb knowledge base |
| `STEWARD_DB_PATH` | `./data/steward.db` | agent-go session store |
| `STEWARD_HTTP_ADDR` | `:7634` | serve listen address |
| `STEWARD_CONV_MEMORY` | `on` | cross-session chat memory |
| `STEWARD_RATE_LIMIT_PER_MIN` | `30` | per-IP LLM-endpoint cap (0 = off) |
| `STEWARD_UNDERSTAND_CMD` | — | command to produce knowledge-graph.json |
| `STEWARD_ALCHEMY_ADDR` | — | alchemy gRPC address; prose extraction goes through alchemy when set |
| `STEWARD_ALCHEMY_TOKEN` / `_TLS` | — / off | its bearer token; TLS on request |

---

## What's standardized vs per-project

- **Fully automatic, any project**: code→graph (`ingest-repo`, auto-salvages a
  stalled understand run), docs→vectors, `serve`, the agent itself
  (domain.toml-driven), deploy (`make deploy`).
- **Finite adapters, pick one**: object-model extraction from a structured source
  (`import-model`: SQL / proto / OpenAPI / C-struct).
- **LLM-assisted, human-reviewed**: drafting `domain.toml` (`steward init`).
- **Irreducible human input**: reviewing/owning the `domain.toml` — above all the
  `red_lines` safety wall — and choosing which structured source is the object
  model. Everything else is the pipeline above.
