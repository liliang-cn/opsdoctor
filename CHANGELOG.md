# Changelog

Versions are git tags. Each entry says what changed and, where it matters, what
was wrong before — a version that only reads as a headline is one nobody can use
to decide whether to upgrade.

## v0.41.0 — 2026-09-06

The hold that justified the alchemy integration could not fire.

v0.39.0 shipped "a document whose sources contradict each other is held", and
that sentence was true of alchemy and false of opsdoctor. alchemy decides a
contradiction from three per-relation flags in the ontology — `both_ways`,
`at_most_one_in`, `at_most_one_out` — and `OntologyFor` translated only names
and ends, so all three arrived false on every relation. Nothing could be a
cardinality conflict, and every symmetric relation was one.

Measured against the live service on the LAN, both halves:

- A document that says a resource group is active on node-b and then corrects
  itself to node-e stored **both** edges and reported no disagreement. It is
  now held — `1 conflict(s), first: resourcegroup:sds-meta -[ACTIVE_ON]->
  node:node-e` — with the vectors stored and the graph withheld.
- A document saying node-b and node-e replicate to each other was **held on a
  question with no right answer**, and stored no edges at all. With
  `both_ways = true` both edges are stored and the job finishes.

The second is the one that mattered: SDS's own `domain.toml` declares two
symmetric relations (`replicates_to`, `conflicts_with`), so any document
describing a healthy DRBD link would have had its whole graph withheld — an
ingest that reports success and writes nothing.

`[[relation]]` now takes the three flags, they reach alchemy, and they are part
of the ontology fingerprint: turning one on changes which graphs the vocabulary
admits, so a graph extracted before the change must not claim to have been
checked after it. `examples/example/domain.toml` gains the ends and flags it
never had, and `opsdoctor domain` refuses overlapping ends as before.

Also: agent-go v3.31.0 -> v3.32.0, cortexdb 2.93.0 -> 2.98.0, alchemy v0.3.0 ->
v0.4.0 (the release where a job that was held and then answered gets its
vectors), qdrant v1.19.1. `make check` green.

Docs record what we actually run: the LLM is `gemini-3.8-flash-high` on the cpa
gateway and the embedder stays on DashScope — they were never required to be
the same provider, and the README said so only by implication.

## v0.40.0 — 2026-09-05

Dependencies only; no source change in this package.

agent-go v3.23.1 -> v3.31.0, cortexdb 2.90.0 -> 2.93.0, alchemy
v0.1.1-0.20260903010501 -> v0.3.0 (the pseudo-version the alchemy work shipped
against is now a release), eval-go -> v0.5.0, agentexec v0.5.0, openai-go
v3.56.0. Through agent-go: grpc 1.81.1 -> 1.83.2 — the release that fixes a
connected client being able to exhaust a server's memory, which matters here
because `serve` is long-lived — plus protobuf 1.36.12, otel 1.44 -> 1.46,
mcp-go v1.0.0, go-sdk v1.7.0, sqlite 1.38.2 -> 1.58.0, qdrant v1.19.0, bleve
2.6.1.

`make check` is green and the existing store still opens and reads under
cortexdb 2.93.0 — 425 sources, 9683 chunks, 6048 nodes, 17546 edges, unchanged.

The go directive stays at 1.25.5. chromedp v0.16.0 and the 2026
go-json-experiment/json both declare `go 1.26`, so agent-go held them back
rather than move the language floor of everything downstream; that decision
lands here as the absence of a bump.

## v0.39.0 — 2026-09-05

Prose can be extracted through alchemy.

Set `OPSDOCTOR_ALCHEMY_ADDR` (and `_TOKEN`, `_TLS`) and every `ingest`,
`ingest-repo` and `refresh` reads prose through alchemy instead of the
built-in per-chunk LLM extractor. One document is one job of its chunks: each
chunk the store embedded goes up as its own source, named by the chunk's id
and read whole, so alchemy's chunk and the store's chunk are the same span
and a citation resolves to a row that exists. The job is checked against the
domain's vocabulary translated into alchemy's ontology — id
`<domain>@<fingerprint>`, ends from `[[relation]]` — and alchemy's own loader
is what the translation is tested against.

What the graph gains: every node and edge carries `alchemy_job`, producer,
model and ontology version, and `alchemy_stated` says whether a model
inferred the fact or a source stated it; edges are flagged inferred. A type
outside the vocabulary, an undecided duplicate and an unread chunk are logged
per document. A document whose sources contradict each other is held: vectors
stored, graph withheld, the job and the conflict named on the log and counted
(`IngestHeld`). A wrong token or an unreachable service says which it is, and
`doctor` has an `alchemy` line that pings with the same token ingest will use.
An unchanged document re-ingested under an unchanged vocabulary is a replay on
the server (idempotency key over ontology id, document id and chunks), not a
second bill — measured: 15 s the first time, 0.4 s the second.

Not used: alchemy's CortexDB connector. It namespaces every node by the run
that produced it, which is right for a graph loaded once and wrong for a base
ingested a document at a time, where the gateway named in forty documents must
be one node. The store's own writer keeps that identity; alchemy's provenance
rides along as metadata. Verified on a two-document corpus: `pool-fast` and
`nfs-export0` are one node each with both documents as sources.

The seam this opened is `knowledge.DocumentExtractor`: the per-chunk LLM path
now sits behind it too (`PerChunk`), the store no longer knows which is
reading, and a held or failed extraction is one line per document either way.

Also: a re-ingest whose extraction fails or is held now keeps the document's
previous graph. The old order purged the graph first and extracted second, so
an alchemy that was unreachable turned a refresh into a wipe — measured on
the same corpus: eighteen nodes, then zero, with "ingested" on stdout. The
graph is replaced only by a new one.

Also: drift no longer counts cortexdb's own `chunk` nodes and `mentions`
edges, which every chunk-linked write produces and no vocabulary will ever
declare — every store with citations read as drifted.

alchemy is a private module today; `go build` of this repository needs access
to it. The dependency is the core module only (proto, wire, ontology), not
the connectors.

## v0.38.0 — 2026-09-05

Renamed: oss-agent is now **opsdoctor**.

"oss" read as object storage to half the people who saw it and as
open-source to the other half, and neither told anyone what the program does.
It reads a product's logs, probes its live state, and answers from a knowledge
graph of that product's own code and docs — an ops doctor, which is also what
its `doctor`, `analyze-log` and `diagnose` commands already called it.

What moved: the GitHub repository (the old URL redirects), the Go module path
`github.com/liliang-cn/opsdoctor`, the root package `opsdoctor`, the CLI
binary, the `OPSDOCTOR_*` environment prefix, the systemd unit and
`/opt/opsdoctor` deploy path in the docs, the web UI title.

What did not: the env fallback — every `OPSDOCTOR_*` variable still reads its
`OSS_*` name, so a host provisioned under the old name starts as before. And
the ontology schema id, which is a storage key every existing knowledge base
holds its schema under; a new id would register a second schema beside the
first rather than replace it. The changelog entries below keep the old name,
because they describe the versions that carried it.

## v0.37.0 — 2026-09-02

agent-go v3.23.1. No change in this package; everything builds and every test
passes against it.

Seven releases since v3.16.0, and one of them reaches here: v3.17.0 fixed a
provider injected through `WithLLM` leaving the service without a model name,
which is exactly how this package hands its provider over, so runs were
priced and traced as an unnamed model. The rest — out-of-process plugins,
background work an agent starts itself, relative time in memory, images to
and from the model, swappable memory backends and a cross-session memory
leak in the file backend (v3.22.1) — is surface this package does not call.

## v0.36.0 — 2026-09-02

Drift stops blaming the extracting model for the imported code graph.

v0.35.0 recorded the finding: point `OSS_DOMAIN_FILE` at any domain and
`doctor` reports `function`, `file`, `imports` and `layer_contains` as types
"the extracting model invented" — on a store where the model had extracted
nothing. Drift compared the whole graph against the prose vocabulary alone,
and the code graph `ingest-repo` writes was never going to be in it. The one
type nobody declared anywhere sat fourth on a list of twenty-six.

The domain already declares the other vocabulary — `[vocabulary.code]`, the
importer's admission list — and the two importers write four structural types
of their own (layers, tour steps, tables, foreign keys) that no domain.toml
names because no domain should. `StoreOptions` now hands the store all of it as
`WithImportedVocabulary`, and `DriftReport` partitions what the prose vocabulary
did not claim into `ImportedNodeTypes`/`ImportedEdgeTypes` and what is still
undeclared. Only the second is drift. The imported match is exact, the way the
importer's own check is: CONTAINS and contains share a word and nothing else.

`doctor` on the local store with the example domain went from a warning naming
26 "invented" types to: graph stays inside the declared vocabulary; 14 imported
code-graph edge types beside it (imports×10377, exports×1597, layer_contains×952,
+11 more). The importers' structural type names are exported constants now so
the list the store is told cannot drift from what the importers write.

## v0.35.0 — 2026-09-02

cortexdb 2.90.0, agent-go v3.16.0. One read moved onto the library; the ontology
was audited and found registered in code but never on this machine's data.

`AllGraph` — the explorer's "draw everything" view — read `graph_nodes` with its
own SQL, the last raw node read left after v0.33.0. 2.90.0 added `ListNodes`,
which is the node read without the vector column, and that is exactly what a
view that wants every node's label and type and none of its embedding should
ask for. It now does. Edges keep their one SQL statement: the library has no
"every edge" read, and adding one for a picture is not worth a library surface.
Checked on a copy of the local store: node, edge, type and file-path counts
match the SQL they replaced.

agent-go v3.12–v3.16 is task memory, extensions and observability; this package
calls none of it and nothing it calls changed shape.

The audit: the ontology machinery is complete — vocabulary-mode schema on open,
interface-first seeding, ends-aware walks, drift and misdirection in `doctor` —
and `data/knowledge.db` holds zero schemas and only code-graph types, because no
`domain.toml` sits where `OSS_DOMAIN_FILE` defaults to. Pointing it at the
example domain registers the schema on first open and drift immediately reports
the imported code graph (`function`, `imports`, `layer_contains` …) as
"invented by the extracting model", which it was not. That mislabel is the next
thing to fix; it is not fixed here.

## v0.34.0 — 2026-08-29

cortexdb 2.87.0. No change in this package.

Picked up because it fixes a real race rather than a test quirk: two processes
opening one PostgreSQL brain at once both passed `CREATE EXTENSION IF NOT
EXISTS` and one lost in the catalogue, which `PostgresStore.Init` turned into a
refusal to start and `initPgVector` turned into exact scans for the life of the
store. This package runs on SQLite, so none of it was reachable here — recorded
so the version this pins is not mistaken for a bump with nothing behind it.

## v0.33.0 — 2026-08-29

cortexdb 2.86.0 — the graph health checks stop writing SQL too, and this package
stops knowing how cortexdb names an entity.

v0.32.0 moved the drift check onto the library and left four raw queries behind,
saying they were a different family of question. Two of them were not: "how much
of the graph is unreachable" and "what are the nodes called" belong to the
library as much as the type census did, so 2.86.0 answers them and the three
sites here now ask.

`checkOrphanNodes` used two statements and divided one by the other, so a write
between them produced a share that was never true — `Connectivity` takes both
numbers from one statement. `checkDuplicateEntities` matched `id LIKE 'entity:%'`,
which is worse than reading cortexdb's tables: it read cortexdb's *naming*, and
nothing would have failed if that changed — the check would have found no
entities and reported every spelling as unique. It now passes
`cortexdb.EntityNodeIDPrefix`, exported for this. Label seeding was the same
query with a length filter instead of a prefix, so it shares the call.

Verified the same way as v0.32.0: on one store the three API answers matched the
SQL they replaced field for field, and both doctor checks still report.

What is left is genuinely a different family — the export dump, the embeddings
inventory, the vector-width probe. None of them is a question about the graph.

## v0.32.0 — 2026-08-29

cortexdb 2.85.0 — the drift check stops writing its own SQL.

v0.31.0 recorded that this package reaches past the API to `graph_nodes` and
`graph_edges`, and that a rewrite underneath those queries is the kind that
breaks quietly: the SQL still parses, still returns rows, and simply stops
describing the graph. cortexdb 2.85.0 answers those four questions itself, so
the four sites in `DriftReport` now ask instead of query — two type censuses,
`EdgeShapes` for the type-shape grouping behind "is this relation backwards",
and `EdgeEndpointPairs` for the nodes behind those edges.

Both joins stay unfiltered even though the library will narrow to named edge
types. The filter matches edge types exactly as stored, and this vocabulary is
matched by fold: a graph holding `Backs` against a declared `backs` would be
filtered out at the database and read as a clean relation — the failure the
check exists to catch, reintroduced by an optimisation.

Verified as a swap and not a rewrite: on one store the API's answers matched the
replaced SQL field for field on all three, and drift still found the backwards
edges. Raw SQL remains in this package for the questions 2.85.0 does not answer
— orphan nodes, entity spellings, the export dump, inventory totals.

## v0.31.0 — 2026-08-29

cortexdb 2.81.0 → 2.84.4, agent-go v3.10.0 → v3.11.1. No change in this package.

Worth recording because 2.82.0 rewrote the graph's SQL to run on both SQLite and
PostgreSQL, and this package reaches past the API to that schema in two places:
`typeCounts` and the three-table join `misdirectedEdges` uses to read an edge
together with both endpoints' types. Both still hold — the drift suite covers
them and passes unchanged. agent-go's additions (a plan store that outlives its
process) and cortexdb's (a live 3D view, fact provenance) are not on any path
this package uses.

## v0.30.1 — 2026-08-27

The node concentration hint reads as one sentence. It rendered "a OCFAgent" and
ran the per-node counts into the total without a break.

## v0.30.0 — 2026-08-27

Drift says which nodes the misdirected edges run through.

`misdirectedEdges` groups by type shape, which answers "how is this relation
wrong" and never "what is wrong". Eighteen non-conforming edges on the live SDS
base came from thirteen nodes, and two carried six of them. Six edges, two places
to look — but reading that took a hand-written join.

The report stops at naming them. Those two needed opposite fixes — one a mistyped
node, the other a misused relation — so a suggested retype would have been wrong
half the time. A node carrying one edge is left out.

## v0.29.0 — 2026-08-27

cortexdb 2.81.0 — resolution keeps two types apart.

`oss-agent resolve` groups by spelling, which is right for "thin pool" and
"thinpool" and wrong for "Primary" the state and "primary" the node. This
package's own graph holds both, one canonical key apart. Merging them would have
ended every `has_state` edge at a Node.

## v0.28.1 — 2026-08-27

The flag rule also covers a flag spelling the text never used.

v0.28.0 caught `--controller` standing in for the controller, where the flag sat
in the text beside the config keys. Nine edges were the opposite case: a design
document describing fields of a `Resource` struct, no flag anywhere in it, and
the model coining `--resource` as the subject. Endpoint resolution then found a
real `--resource` node from the ingested CLI help and hung nine edges off it. The
rule now also says to call an entity what the text calls it.

## v0.28.0 — 2026-08-27

A flag is not the thing it names, and a count is not a shape.

Twelve of the twenty edges doctor still complained about were `--resource
configured_by WANMode` and its like. The model had the relation, the direction
and the target right, and wrote the flag that selects a resource where the
resource belongs. This package ingests a binary's `--help` as a first-class
source, so its material is full of flags sitting beside the things they
configure; the prompt now says a flag names something without being it.

## v0.27.0 — 2026-08-26

Re-ingesting a document replaces its graph instead of adding to it.

Chunk ids derive from the document id, so a re-ingest overwrites its vectors and
the chunk count never moves — which is why this looked like replacement. An
edge's id includes both endpoints, so an extraction that reads the text
differently writes a new edge beside the old one. On the live SDS base one
`protects` edge stood in three versions at once. Two re-ingests of sixteen
documents took the graph from 5850 edges to 7277 while the chunk count stayed at
1586 — and "re-ingest the sources these came from" was doctor's only suggested
remedy.

## v0.26.0 — 2026-08-26

cortexdb 2.80.0 — a relation endpoint resolves to the domain's entity.

This package builds exactly the store the fix is about: a prose vocabulary and a
code graph in one database. Both held a "Snapshot" and a "Volume" — the domain's
entity and a Go type — and an endpoint resolved to whichever id sorted first,
which was the code one. Five edges from the SDS runbooks landed on structs and
arrived as drift against `protects` rather than as the resolution error they
were.

## v0.25.0 — 2026-08-26

The extraction prompt states which way a relation runs.

`[[relation]] from/to` was read by the ontology's link sides, by drift, and by
the typed walk — by everything except the one place a wrong direction can be
prevented instead of detected. A reversed edge was extracted, stored, registered
against, reported by doctor, and excluded from traversal; re-ingesting only
re-rolled the dice. Relations the domain left open are still offered and said to
be open — dropping them would quietly shrink the vocabulary, and inventing ends
for them would be a lie.

## v0.24.0 — 2026-08-26

A flag is not a spelling of an identifier.

`spellingKey` dropped punctuation, so `--read-only` and `ReadOnly` pooled: a walk
asked about the API field answered with the flag's neighbours. Five of the
eighteen merges the live SDS base proposed were this shape, and node type did not
separate them — all five pairs were ConfigParameter, so only the leading dash
could.

## v0.23.0 — 2026-08-26

One concept spelled two ways stops being two answers.

Entity ids keep separators, so "DRBDResource" lands on `entity:drbdresource` and
"DRBD resource" on `entity:drbd_resource`. Eight pairs had split this way on the
SDS base. The walk made it an answer rather than a miss: `FindNodes` returns
every candidate it reached, `Walk` asked for Limit 1 and took the first. So
"DRBDResource" found Backup and "DRBD resource" reported that nothing protects
it — an empty result phrased as a finding about the graph.

## v0.22.0 — 2026-08-26

An unspecified mention no longer demotes a declared type. cortexdb 2.73.0 →
2.77.0.

Entity nodes upsert with `ON CONFLICT ... SET node_type = excluded.node_type`, so
the last write wins on type. An extractor that recognises a name without placing
it emits no type, and vocabulary mode canonicalises the blank onto the open
Entity supertype — a type this package declares itself, so nothing downstream
rejected it and drift could not see it.

## v0.21.0 — 2026-08-23

`graph_walk` — the declared relation ends become a runtime dependency.

The link types the ontology registers had no reader: `[[relation]] from/to` was a
record, and deleting the code that built them would not have changed a single
answer. There was one honest fix, and it was to add the missing capability rather
than route an existing path through the ontology for appearance's sake.

`knowledge_search` finds text ABOUT something and returns whatever is one hop
away — right for "why did failover stall", wrong for "which pools back this
resource", which has an exact answer sitting in the graph. A relation that
declared its ends is walked through its link sides, so each result is a node of
the far end's declared type; the misdirected edges drift reports are exactly the
ones a typed walk leaves out. A relation with open ends falls back to following
edges and says so.

## v0.20.0 — 2026-08-23

A graph neighbour says which way its edge runs.

A one-hop neighbour reached the model as `{name: "tank", via: "backs"}` — the
edge type, and nothing about which end the retrieved chunk was on. "This resource
backs tank" and "tank backs this resource" arrived identical, and the model
picked one. It read exactly as well either way, which is the failure the whole
relation-ends apparatus was built to catch, sitting one layer below it in the
path that feeds every answer.

## v0.19.0 — 2026-08-23

An imported code edge is no longer judged against the prose vocabulary.

A knowledge base holds two vocabularies with one namespace between them.
`exports` means "a Node exports a Gateway" in prose and "a file exports a
function" in the code graph. The check reported 452 imported edges on a live SDS
base as backwards — real data, correctly imported, filed as a defect, and enough
of it to bury the eleven true findings underneath. Both ends must now be declared
entity types for an edge to be judged.

## v0.18.0 — 2026-08-23

A relation's end may be several types, so a polymorphic one can be declared.

An end had to be one entity type, which left the relations an ops vocabulary is
mostly made of undeclarable: a Snapshot and a Backup both protect a Volume, seven
kinds of thing have a State. They stayed in `relation_types` with no ends — no
direction, nothing to disagree with the extractor, on exactly the relations where
it is most likely to get the direction wrong. cortexdb v2.73.0 lets a link side
name an interface, so `from` and `to` now take a string or a list.

## v0.17.0 — 2026-08-23

Relations can declare their ends, and a backwards edge stops being invisible.

`relation_types` declares edge names and nothing else, so a registered link
pointed both sides at one open "Entity" supertype standing for "unconstrained".
That is not a neutral placeholder: cortexdb decides a traversal's direction by
keeping only nodes of the far side's type, so every ends-aware traversal returned
the empty set while the schema read as complete — the precise failure the
ontology was registered to prevent, sitting inside the registration itself.

## v0.16.0 — 2026-08-23

The declared vocabulary reaches retrieval, and drift stops being invisible.

The ontology was recorded and never asked. The one place that wanted "is this
node something the domain declares" carried a literal list of twelve LINSTOR type
names in an `ORDER BY`, transcribed from one product's domain.toml before domains
could supply their own — and by now not even that product matched it. Every
declared entity type now implements a `DomainEntity` interface, which expands to
its implementors wherever a type filter is accepted.

## v0.15.1 — 2026-08-16

Chunk the ingest CLI's help per command, not per four.

## v0.15.0 — 2026-08-16

Make the copilot's failure modes structural, not per-project.

## v0.14.0 — 2026-08-16

Retrieve prose and code separately, so one cannot bury the other.

## v0.13.0 — 2026-08-15

Purge reaches the graph, the ontology activates as vocabulary, ingest stops
eating garbage.

## v0.12.1 — 2026-08-15

An active ontology schema refused every entity upsert; register it deactivated,
and stop discarding ingest errors.

## v0.12.0 — 2026-08-15

Configurable `source_patterns` — mine types and concepts from source, not only
error strings.

## v0.11.0 — 2026-08-15

Go sources are mined for error strings; neighbours capped per entity type.

## v0.10.0 — 2026-08-15

Graph expansion actually reaches the graph.

## v0.9.0 — 2026-08-15

Domain ontology registered in cortexdb, drift reported.

## v0.8.0 — 2026-08-15

Traverse the edges the domain declares.

## v0.7.0 — 2026-08-15

The nearest chunk is no longer presented as a relevant one.

## v0.6.2 — 2026-08-15

cortexdb v2.68.0 — hyphenated terms reach the keyword arm.

## v0.6.1 — 2026-08-15

Inventory reports the index's real vector width (was off by cortexdb's 4-byte
header).

## v0.6.0 — 2026-08-15

Session-aware streaming, annotation-gated tools, KB inventory, honest empty
retrieval.

## v0.5.1 — 2026-08-15

The read-only gate judges the operation, not the subject.

## v0.5.0 — 2026-08-15

agent-go v3 + cortexdb v2.67 + openai-go v3.51.

## v0.4.3 — 2026-08-02

cortexdb v2.62.1 + eval-go v0.4.0 + eval nil-deref fix.

## v0.4.2 — 2026-08-02

agent-go v2.107.0 + cortexdb v2.45.0 — MCP tooling, `suggest_action`, KB update
methods.

## v0.4.1 — 2026-07-08

Knowledge-base update methods on the Agent facade.

## v0.4.0 — 2026-07-09

Superseded by v0.4.1.

## v0.3.0 — 2026-07-02

Library facade, RAG rerank and citations, eval harness, live tool stream, 3D
graph.

## v0.2.0 — 2026-06-20

oss-agent v0.2.0.
