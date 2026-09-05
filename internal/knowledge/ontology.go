package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"

	cortexdb "github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// The domain's ontology used to exist in exactly two places, neither of which
// could check anything:
//
//   - as prose pasted into the extraction prompt, telling the model what to emit;
//   - as a list of edge type strings handed to graph expansion at query time.
//
// So nothing recorded which vocabulary a graph had been extracted under, and
// nothing rejected a type outside it. Both cost real time. A graph extracted
// under a SCREAMING_SNAKE relation set kept expanding to nothing long after the
// vocabulary changed, and the only way to find out was to read the traversal
// filter's source. And a live SDS knowledge base carried a "StorageClass" node
// type the domain never declared — invented by the extracting model, stored
// without complaint, invisible until someone ran a GROUP BY by hand.
//
// cortexdb has had somewhere to put this the whole time: a versioned,
// activatable ontology schema, with object types and link types that carry
// which object type sits on each end. Registering the vocabulary there makes it
// a record rather than a convention — one that says what a graph was built
// under, and that drift can be measured against.

// ontologySchemaID is the stable id the domain's schema is stored under. One
// knowledge base holds one domain, so this does not need to vary; re-registering
// replaces it and cortexdb carries the version.
//
// It still spells the program's old name on purpose. This is a storage key,
// not a label: every knowledge base built before the rename holds its schema
// under it, and a new id would register a second schema beside the first
// rather than replace it. The name a person sees is the schema's Name field.
const ontologySchemaID = "oss-agent-domain"

// WithOntology registers the domain's declared vocabulary as a cortexdb
// ontology schema when the store opens, and makes DriftReport meaningful.
//
// Registration is best-effort: a knowledge base that cannot store the schema
// still ingests and still answers, because refusing to serve over bookkeeping
// would trade a real capability for a record-keeping one.
func WithOntology(name string, entityTypes, relationTypes []string) Option {
	return func(s *Store) {
		s.ontologyName = strings.TrimSpace(name)
		s.entityTypes = trimAll(entityTypes)
		s.relationTypes = trimAll(relationTypes)
	}
}

// RelationEnd is an edge type whose ends the domain declared. Each end is one
// or more entity types. See WithRelationEnds.
type RelationEnd struct {
	Name string
	From []string
	To   []string
}

// endInterfaceName is what a multi-type end is registered as.
//
// cortexdb links are one name and one pair of ends, and a side names one type —
// but that type may be an interface, which is how a polymorphic end is modelled.
// So an end listing several types becomes an interface those types implement,
// named after the relation and the side so it reads in an error message: a
// schema rejection naming "protects_source" points at the `from` of `protects`.
//
// Derived rather than declared because the domain has no reason to name it. It
// exists to make one relation's end sayable, not to be a concept in the
// vocabulary.
func endInterfaceName(relation, side string) string {
	return strings.ReplaceAll(relation, " ", "_") + "_" + side
}

// WithRelationEnds tells the ontology what sits on each end of the relations
// that have one.
//
// A link type registered without ends has to name SOMETHING on each side, so
// both sides pointed at the open Entity supertype — and cortexdb decides a
// traversal's direction by keeping only nodes of the far side's type. With
// Entity on the far side that keeps only nodes whose type is literally
// "Entity", so every ends-aware traversal over this vocabulary returned nothing
// while the schema read as complete.
//
// Only the relations named here get real ends; the rest keep the supertype and
// keep behaving as before. That is the honest encoding of a domain.toml that
// declares ends for some of its edges and not others.
func WithRelationEnds(ends []RelationEnd) Option {
	return func(s *Store) {
		s.relationEnds = make(map[string]RelationEnd, len(ends))
		for _, e := range ends {
			e.Name, e.From, e.To = strings.TrimSpace(e.Name), trimAll(e.From), trimAll(e.To)
			if e.Name == "" || len(e.From) == 0 || len(e.To) == 0 {
				continue
			}
			s.relationEnds[strings.ToLower(e.Name)] = e
		}
	}
}

func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return dedupe(out)
}

// entityType is the open object type both ends of every link point at.
//
// It exists to state "unconstrained" in a form cortexdb will accept. A
// domain.toml declares relations as bare names — "backs", "exports" — with no
// statement of what may sit on either side, and cortexdb requires every link
// side to name an object type. Inventing a domain and range per relation would
// be asserting a constraint the domain never made and then rejecting
// extractions on the strength of the guess. Pointing both ends at a supertype
// every entity implements says the true thing instead: any declared entity may
// sit on either side. When a domain gains a way to express ends, they replace
// this and cardinality checking becomes meaningful.
//
// It is an object type rather than an interface because cortexdb refuses a
// schema whose interface name collides with an object type name, and link sides
// name object types.
const entityInterface = "Entity"

// domainEntityInterface is what every declared entity type implements, and it is
// the vocabulary's one retrieval-time consequence.
//
// Registering object types recorded the vocabulary but could not be *asked*
// anything: "which of these nodes are things the domain declared" had no answer
// short of an IN list assembled by hand, and the one place that wanted it — the
// graph explorer's seed query — carried a literal list of twelve LINSTOR type
// names, so a second product's own entities sorted below the first product's.
//
// An interface is the form cortexdb can answer that question in. It expands to
// its implementors wherever a type filter is accepted (FindNodes, search,
// expand), so asking for DomainEntity asks for the active domain's vocabulary,
// whatever that domain happens to be. Nothing is stored under this name; it is a
// question, not a type.
//
// The Entity anchor implements it too, because a chunk whose extraction omitted
// a type is stored as `entity` — still something the domain's extractor pulled
// out of prose, and still not an imported code node, which is the distinction
// this interface exists to draw.
const domainEntityInterface = "DomainEntity"

// Two fingerprints are stored on the schema, and the difference between them is
// the whole point.
//
// vocabularyFingerprintKey is what domain.toml declared the last time the store
// was opened. extractionFingerprintKey is what it declared when entities were
// last written INTO the graph, and it is only ever set by an ingest.
//
// Comparing the first against the live domain.toml can never fail: registration
// runs at Open, so by the time anything reads it back it has already been
// overwritten with the value it is about to be compared to. The check read as a
// real one and could not fire. The graph, meanwhile, still holds edges spelled
// the old way, expansion still filters them all out, and the symptom is still an
// answer that is merely thinner than it should be.
const (
	vocabularyFingerprintKey = "vocabulary_fingerprint"
	extractionFingerprintKey = "extraction_fingerprint"
)

// extractionFingerprintOf reads the extraction record off a stored schema, or
// "" when there is none — an empty record means nothing has been extracted yet,
// which is not drift.
func extractionFingerprintOf(stored *cortexdb.OntologySchema) string {
	if stored == nil {
		return ""
	}
	return stored.Metadata[extractionFingerprintKey]
}

// storedOntology returns the schema already in the database, or nil.
func (s *Store) storedOntology(ctx context.Context) *cortexdb.OntologySchema {
	got, err := s.db.GetOntologySchema(ctx, cortexdb.OntologyGetRequest{SchemaID: ontologySchemaID})
	if err != nil || got == nil {
		return nil
	}
	return &got.Schema
}

// ontologyIsCurrent reports whether the stored schema already IS this
// vocabulary, active and non-gating.
//
// cortexdb derives a schema's version on every save — a stored one always
// becomes version+1 — and every command that opens the store registers. Without
// this the version counter measured how many times someone ran `search`, and a
// read-only command wrote to the database to learn nothing.
func (s *Store) ontologyIsCurrent(stored *cortexdb.OntologySchema) bool {
	return stored != nil &&
		stored.Active &&
		stored.Enforcement == cortexdb.OntologyEnforcementVocabulary &&
		stored.Metadata[vocabularyFingerprintKey] == s.vocabularyFingerprint()
}

// noteExtractionVocabulary records that entities have just been written under
// the vocabulary now loaded. Called after a successful entity upsert, and a
// no-op when the record already says so — which is every ingest but the first
// after a domain.toml change.
//
// Best-effort, like everything else about the registration: an ingest that
// cannot update the record has still grown the graph, and refusing to ingest
// over bookkeeping would trade the capability for the record of it.
func (s *Store) noteExtractionVocabulary(ctx context.Context) {
	if len(s.entityTypes) == 0 && len(s.relationTypes) == 0 {
		return
	}
	stored := s.storedOntology(ctx)
	if stored == nil {
		return
	}
	current := s.vocabularyFingerprint()
	if stored.Metadata[extractionFingerprintKey] == current {
		return
	}
	if stored.Metadata == nil {
		stored.Metadata = map[string]string{}
	}
	stored.Metadata[extractionFingerprintKey] = current
	if _, err := s.db.SaveOntologySchema(ctx, cortexdb.OntologySaveRequest{
		Activate: true, Schema: *stored,
	}); err != nil {
		log.Printf("[opsdoctor] knowledge: recording the extraction vocabulary failed: %v", err)
	}
}

// registerOntology stores the declared vocabulary and activates it in
// vocabulary mode — canonical spellings and interface retrieval, no write
// gating.
func (s *Store) registerOntology(ctx context.Context) error {
	if len(s.entityTypes) == 0 && len(s.relationTypes) == 0 {
		return nil
	}
	stored := s.storedOntology(ctx)
	if s.ontologyIsCurrent(stored) {
		return nil
	}

	// Every object type carries the properties a graph node actually has.
	//
	// "id" is here because cortexdb validates that primary_key names a DECLARED
	// property, so an object type naming "id" without declaring it is rejected
	// and the whole schema fails to register — silently, since registration is
	// best-effort.
	//
	// The other two are `name` and `description` because those are the keys
	// cortexdb writes into a node's properties JSON when an extracted entity is
	// upserted, and a property predicate reads properties, not columns. A
	// "content" property was declared here for a while and matched nothing: the
	// text is in the graph_nodes.content COLUMN, which no predicate can see, so
	// every filter over it silently selected the empty set.
	props := []cortexdb.OntologyProperty{
		{
			APIName:     "id",
			DisplayName: "Node id",
			Description: "Identity of the extracted graph node in the knowledge store.",
			DataType:    cortexdb.OntologyDataType{Kind: cortexdb.OntologyDataString},
			Required:    true,
		},
		{
			APIName:     "name",
			DisplayName: "Name",
			Description: "What the entity is called in the text it was extracted from.",
			DataType:    cortexdb.OntologyDataType{Kind: cortexdb.OntologyDataString},
			Searchable:  true,
		},
		{
			APIName:     "description",
			DisplayName: "Description",
			Description: "The one-line description the extractor produced for the entity.",
			DataType:    cortexdb.OntologyDataType{Kind: cortexdb.OntologyDataString},
			Searchable:  true,
		},
	}

	implements := []string{domainEntityInterface}
	// A link's two sides are the traversal's two directions: cortexdb keeps only
	// nodes of the FAR side's type, so the side names are what make "backs" and
	// "backs_of" mean opposite things. Ends the domain declared go in as
	// themselves; the rest keep the open supertype, which is a traversal that
	// reaches nothing and an honest statement that the domain said nothing.
	//
	// An end listing several types becomes an interface those types implement —
	// a side names one type, but that type may be an interface, which is how
	// cortexdb models a polymorphic end. So the object types have to be built
	// after the links, because building the links is what discovers which
	// generated interfaces each type belongs to.
	interfaces := []cortexdb.OntologyInterfaceType{{
		APIName:     domainEntityInterface,
		DisplayName: "Domain entity",
		Description: "Anything the active domain's vocabulary declares. Ask for it by name wherever a node type filter is accepted and it expands to every declared type.",
	}}
	// implementedBy maps an entity type to every interface it implements: the
	// DomainEntity one everything gets, plus a generated one per polymorphic end
	// it appears in.
	implementedBy := make(map[string][]string, len(s.entityTypes))

	end := func(relation, side string, types []string) string {
		if len(types) == 1 {
			return types[0]
		}
		name := endInterfaceName(relation, side)
		interfaces = append(interfaces, cortexdb.OntologyInterfaceType{
			APIName:     name,
			DisplayName: name,
			Description: fmt.Sprintf("The %s end of %q: any of %s.", side, relation, strings.Join(types, ", ")),
		})
		for _, t := range types {
			implementedBy[strings.ToLower(t)] = append(implementedBy[strings.ToLower(t)], name)
		}
		return name
	}

	links := make([]cortexdb.OntologyLinkType, 0, len(s.relationTypes))
	for _, t := range s.relationTypes {
		from, to := entityInterface, entityInterface
		if e, ok := s.relationEnds[strings.ToLower(t)]; ok {
			from = end(e.Name, "source", e.From)
			to = end(e.Name, "target", e.To)
		}
		links = append(links, cortexdb.OntologyLinkType{
			APIName: t,
			A: cortexdb.OntologyLinkSide{
				APIName:           t,
				ObjectTypeAPIName: from,
				Cardinality:       cortexdb.OntologyCardinalityMany,
			},
			B: cortexdb.OntologyLinkSide{
				APIName:           t + "_of",
				ObjectTypeAPIName: to,
				Cardinality:       cortexdb.OntologyCardinalityMany,
			},
		})
	}

	objects := make([]cortexdb.OntologyObjectType, 0, len(s.entityTypes)+1)
	objects = append(objects, cortexdb.OntologyObjectType{
		APIName:     entityInterface,
		DisplayName: "Entity",
		Description: "Any extracted graph node. Both ends of a link whose ends the domain did not declare point here.",
		Properties:  props,
		PrimaryKey:  "id",
		Implements:  implements,
	})
	for _, t := range s.entityTypes {
		if strings.EqualFold(t, entityInterface) {
			continue
		}
		objects = append(objects, cortexdb.OntologyObjectType{
			APIName:     t,
			DisplayName: t,
			Properties:  props,
			PrimaryKey:  "id",
			Implements:  dedupe(append(append([]string{}, implements...), implementedBy[strings.ToLower(t)]...)),
		})
	}

	name := s.ontologyName
	if name == "" {
		name = "opsdoctor domain"
	}
	// Activated as a VOCABULARY schema, never a strict one. A strict active
	// schema is enforced on every entity upsert, and that enforcement demands
	// what an LLM extractor does not produce — each entity carrying its object
	// type's primary key. Activating one froze every graph write on a live
	// deployment, silently, because IngestSemantic discards the upsert error;
	// the workaround for a while was to register the schema deactivated, which
	// kept ingest alive at the price of everything activation is for.
	//
	// enforcement=vocabulary (cortexdb ≥2.69) is the resolution: the schema
	// canonicalizes declared type spellings and powers interface retrieval,
	// while keyless and undeclared entities — all of LLM extraction, and every
	// code-graph import — fall back to name-derived ids exactly as if no
	// schema were active. Same node ids as before activation, so existing
	// graphs do not fork. The registration doubles as the record: a versioned,
	// fingerprinted statement of the vocabulary, and the DriftReport baseline.
	_, err := s.db.SaveOntologySchema(ctx, cortexdb.OntologySaveRequest{
		Activate: true,
		Schema: cortexdb.OntologySchema{
			SchemaID:    ontologySchemaID,
			Name:        name,
			Enforcement: cortexdb.OntologyEnforcementVocabulary,
			Description: "Entity and relation vocabulary declared by the active domain.toml.",
			Metadata: map[string]string{
				// The fingerprint is what makes a graph traceable to a
				// vocabulary: compare it against the stored schema and a
				// domain.toml that changed since the last ingest is visible
				// instead of being something you deduce from bad answers.
				vocabularyFingerprintKey: s.vocabularyFingerprint(),
				// Carried across, never set here. This registration is a
				// statement about domain.toml; only an ingest can say what the
				// GRAPH was built under, and overwriting it here is what made
				// the comparison compare a value with itself.
				extractionFingerprintKey: extractionFingerprintOf(stored),
				"entity_type_count":      fmt.Sprint(len(s.entityTypes)),
				"relation_type_count":    fmt.Sprint(len(s.relationTypes)),
			},
			ObjectTypes:    objects,
			LinkTypes:      links,
			InterfaceTypes: interfaces,
		},
	})
	if err != nil {
		return fmt.Errorf("register ontology schema: %w", err)
	}
	return nil
}

// vocabularyFingerprint is a stable digest of the declared vocabulary. Order
// does not change it, so reordering domain.toml is not mistaken for a change of
// meaning.
func (s *Store) vocabularyFingerprint() string {
	ents := append([]string(nil), s.entityTypes...)
	rels := append([]string(nil), s.relationTypes...)
	sort.Strings(ents)
	sort.Strings(rels)

	// The ends are part of the vocabulary, not decoration on it. Leaving them
	// out means changing what may sit on either end of a relation does not
	// change the fingerprint — so the registration short-circuit decides the
	// schema is already current and the new ends are never written, while drift
	// goes on reporting a graph as matching a vocabulary it no longer does.
	ends := make([]string, 0, len(s.relationEnds))
	for _, e := range s.relationEnds {
		// The types within one end are sorted for the same reason the
		// vocabularies are: listing them in a different order is not a
		// different statement about the domain.
		from := append([]string(nil), e.From...)
		to := append([]string(nil), e.To...)
		sort.Strings(from)
		sort.Strings(to)
		ends = append(ends, e.Name+"\x1f"+strings.Join(from, ",")+"\x1f"+strings.Join(to, ","))
	}
	sort.Strings(ends)

	sum := sha256.Sum256([]byte(strings.Join(ents, "\x00") +
		"\x1e" + strings.Join(rels, "\x00") +
		"\x1e" + strings.Join(ends, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// declaresNodeType reports whether the domain's vocabulary contains this node
// type, comparing case-insensitively for the reason diffVocabulary does: the
// type in the graph is whatever the extracting model emitted, and "Contains" is
// the declared "contains" with a capital letter.
//
// A domain that declared no vocabulary declares everything: with nothing to
// prefer, preferring nothing is what keeps such a domain behaving as before.
func (s *Store) declaresNodeType(t string) bool {
	if len(s.entityTypes) == 0 {
		return true
	}
	for _, declared := range s.entityTypes {
		if strings.EqualFold(declared, t) {
			return true
		}
	}
	return false
}

// seedsByName resolves a query to graph node ids by what the nodes are called,
// asking for the domain's own vocabulary first and only then for anything else.
//
// This is the ontology's one retrieval-time consequence, and it replaced a
// literal list of twelve LINSTOR type names in an ORDER BY — a per-project
// constant that sorted a second product's entities below the first product's,
// and that could not be told a domain had changed. Asking for
// domainEntityInterface asks cortexdb to expand the ACTIVE schema, so the
// preference follows domain.toml with nothing transcribed.
//
// Two passes rather than one filtered query: an explorer that could only reach
// declared entities could not show the imported code graph at all, which is
// half of what there is to explore. Declared first, the rest behind it.
//
// Best-effort throughout. A seed set is an optimization over "expand from
// nothing", so a lookup that fails returns what it has.
func (s *Store) seedsByName(ctx context.Context, query string, limit int) []string {
	if strings.TrimSpace(query) == "" || limit <= 0 {
		return nil
	}
	seen := make(map[string]struct{}, limit)
	out := make([]string, 0, limit)

	collect := func(nodeTypes []string) {
		if len(out) >= limit {
			return
		}
		res, err := s.tb.FindNodes(ctx, cortexdb.ToolFindNodesRequest{
			Names:     []string{query},
			NodeTypes: nodeTypes,
			Limit:     limit,
		})
		if err != nil {
			log.Printf("[opsdoctor] knowledge: name seeding failed: %v", err)
			return
		}
		for _, m := range res.Matches {
			for _, n := range m.Nodes {
				if n == nil || n.ID == "" {
					continue
				}
				if _, dup := seen[n.ID]; dup {
					continue
				}
				seen[n.ID] = struct{}{}
				out = append(out, n.ID)
				if len(out) >= limit {
					return
				}
			}
		}
	}

	// A domain that declared no vocabulary registers no schema, so the
	// interface resolves to nothing and the filtered pass would only cost a
	// scan. Skipping it also keeps such a domain behaving exactly as before.
	if len(s.entityTypes) > 0 {
		collect([]string{domainEntityInterface})
	}
	collect(nil)
	return out
}

// OntologyDrift is what the graph holds that the domain never declared, and
// what it declared that never appeared.
type OntologyDrift struct {
	// Registered is false when no vocabulary was declared, in which case the
	// other fields say nothing.
	Registered bool `json:"registered"`
	// Fingerprint identifies the vocabulary now loaded from domain.toml.
	Fingerprint string `json:"fingerprint,omitempty"`
	// StoredFingerprint is the vocabulary the GRAPH was extracted under — the
	// one in force when entities were last written. A difference means the graph
	// holds edges spelled the old way, expansion filters every one of them out,
	// and GraphRAG has quietly become plain vector search. Empty until something
	// has been ingested, which is not drift.
	StoredFingerprint string `json:"stored_fingerprint,omitempty"`

	// UndeclaredNodeTypes and UndeclaredEdgeTypes are in no vocabulary at all
	// — not the prose one, not the imported one — which leaves the extracting
	// model as the only thing that could have written them. Undeclared edges
	// are the expensive ones: expansion filters on the declared vocabulary, so
	// they are stored and never walked.
	UndeclaredNodeTypes map[string]int `json:"undeclared_node_types,omitempty"`
	UndeclaredEdgeTypes map[string]int `json:"undeclared_edge_types,omitempty"`

	// ImportedNodeTypes and ImportedEdgeTypes are outside the prose vocabulary
	// and inside the imported one: the code graph `ingest-repo` and
	// `import-schema` wrote. Not drift — every one of them was declared,
	// somewhere other than entity_types — but counted, because on most stores
	// they are the bulk of the graph and a report that hid them read as if the
	// graph were nearly empty. See WithImportedVocabulary.
	ImportedNodeTypes map[string]int `json:"imported_node_types,omitempty"`
	ImportedEdgeTypes map[string]int `json:"imported_edge_types,omitempty"`

	// UnusedNodeTypes and UnusedEdgeTypes were declared and never extracted.
	// Harmless, but they are prompt budget spent on nothing, and a type the
	// model never reaches for is usually one the corpus does not contain.
	UnusedNodeTypes []string `json:"unused_node_types,omitempty"`
	UnusedEdgeTypes []string `json:"unused_edge_types,omitempty"`

	// MisdirectedEdges are edges whose real ends contradict the ends the domain
	// declared for that relation — most often the relation asserted backwards,
	// which is the single most common thing an extracting model gets wrong and
	// the one a reader cannot detect from the answer. Only relations that
	// declared ends can appear here; the rest have nothing to contradict.
	MisdirectedEdges []MisdirectedEdge `json:"misdirected_edges,omitempty"`

	// MisdirectedNodes are the nodes those edges run through, worst first. A
	// relation wrong twenty times is often one node wrong once: a name the
	// graph typed as one thing and the material means as another sits on every
	// edge that mentions it. Naming them turns a list of edge complaints into a
	// short list of things to look at.
	MisdirectedNodes []MisdirectedNode `json:"misdirected_nodes,omitempty"`
}

// MisdirectedEdge is one relation's worth of edges that do not match its
// declared ends.
type MisdirectedEdge struct {
	Relation string `json:"relation"`
	// Want is the declared shape, Got the most common shape actually stored.
	Want string `json:"want"`
	Got  string `json:"got"`
	// Count is how many edges of this relation are wrong, Reversed how many of
	// those are exactly backwards. A relation entirely reversed is one edit to
	// domain.toml; one wrong in fifty is the model having a bad day.
	Count    int `json:"count"`
	Reversed int `json:"reversed"`
	// GotCount is how many of Count have the shape Got. A relation can be wrong
	// in several ways at once, and reporting the total beside the commonest
	// shape reads as a claim about every one of them.
	GotCount int `json:"got_count,omitempty"`
}

// MisdirectedNode is one node that several misdirected edges pass through.
//
// Deliberately carries no suggested type. Of the two nodes accounting for six
// misdirected edges on a live base, one was genuinely mistyped — the source
// calls `service-ip` a flag and the graph called it a command — and the other,
// `OCF agents`, was typed correctly and simply is not a configuration
// parameter. They need opposite fixes, so this says where to look and leaves
// the judgement where it belongs.
type MisdirectedNode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	// Edges is how many misdirected edges have this node on the offending end.
	Edges int `json:"edges"`
}

// Clean reports whether the graph stayed inside the declared vocabulary.
func (d OntologyDrift) Clean() bool {
	return len(d.UndeclaredNodeTypes) == 0 &&
		len(d.UndeclaredEdgeTypes) == 0 &&
		len(d.MisdirectedEdges) == 0
}

// DriftReport compares the types actually in the graph against the vocabulary
// the domain declared.
//
// Extraction is done by a language model, so drift is not a failure mode to be
// prevented — it is the expected behaviour of the component, and the only
// question is whether anyone finds out. This is how they find out.
func (s *Store) DriftReport(ctx context.Context) (*OntologyDrift, error) {
	d := &OntologyDrift{}
	if len(s.entityTypes) == 0 && len(s.relationTypes) == 0 {
		return d, nil
	}
	d.Registered = true
	d.Fingerprint = s.vocabularyFingerprint()

	d.StoredFingerprint = extractionFingerprintOf(s.storedOntology(ctx))

	nodeCounts, err := namedTypeCounts(s.db.Graph().NodeTypeCounts(ctx))
	if err != nil {
		return nil, err
	}
	edgeCounts, err := namedTypeCounts(s.db.Graph().EdgeTypeCounts(ctx))
	if err != nil {
		return nil, err
	}

	// cortexdb's own bookkeeping types are nobody's vocabulary: a `chunk`
	// node per cited chunk and a `mentions` edge from it to the entity, both
	// written by the store on every chunk-linked upsert. Counting them
	// reported every store with citations as drifted.
	for _, t := range cortexdbOwnNodeTypes {
		delete(nodeCounts, t)
	}
	for _, t := range cortexdbOwnEdgeTypes {
		delete(edgeCounts, t)
	}
	d.UndeclaredNodeTypes, d.UnusedNodeTypes = diffVocabulary(s.entityTypes, nodeCounts)
	d.UndeclaredEdgeTypes, d.UnusedEdgeTypes = diffVocabulary(s.relationTypes, edgeCounts)
	// The prose vocabulary is asked first and case-insensitively, as it always
	// was; what it does not claim is then offered to the imported vocabulary.
	// The order matters where the two share a word: a `contains` under a
	// declared CONTAINS stays the prose vocabulary's, which is what expansion
	// treats it as.
	d.UndeclaredNodeTypes, d.ImportedNodeTypes = partitionImported(d.UndeclaredNodeTypes, s.importedEntityTypes)
	d.UndeclaredEdgeTypes, d.ImportedEdgeTypes = partitionImported(d.UndeclaredEdgeTypes, s.importedRelationTypes)

	misdirected, err := s.misdirectedEdges(ctx)
	if err != nil {
		return nil, err
	}
	d.MisdirectedEdges = misdirected
	if nodes, err := s.misdirectedNodes(ctx); err != nil {
		log.Printf("[opsdoctor] knowledge: finding the nodes behind misdirected edges failed: %v", err)
	} else {
		d.MisdirectedNodes = nodes
	}
	return d, nil
}

// misdirectedEdges finds edges whose real ends contradict the declared ones.
//
// This is what declaring ends buys, and the reason it is worth a line in
// domain.toml. An extracted `backs` asserted from the resource to the pool is
// stored without complaint, reads as a fact, and is walked as one; nothing in
// the answer it produces says the arrow points the wrong way. The declaration
// is the only thing that can disagree with it.
//
// Reversed is counted separately because the two findings mean different
// things. A relation reversed on every edge is one wrong line in domain.toml or
// one confusing sentence in the extraction prompt — a fix. A handful wrong out
// of many is the model, and the fix is re-ingesting the sources, if anything.
func (s *Store) misdirectedEdges(ctx context.Context) ([]MisdirectedEdge, error) {
	if len(s.relationEnds) == 0 {
		return nil, nil
	}
	// Every shape, not only the declared relations the library could filter to.
	// The filter matches edge types exactly as stored, and this vocabulary is
	// matched case-insensitively — a graph holding `Backs` against a declared
	// `backs` would be filtered out at the database and read as a clean
	// relation, which is the failure this whole check exists to catch.
	shapes, err := s.db.Graph().EdgeShapes(ctx)
	if err != nil {
		return nil, fmt.Errorf("check relation ends: %w", err)
	}

	// Accumulated per relation rather than per shape: an operator needs "backs
	// is backwards", not eleven rows of type pairs to compare by eye.
	type tally struct {
		want            string
		count, reversed int
		shapes          map[string]int
	}
	byRelation := make(map[string]*tally)

	for _, shape := range shapes {
		edgeType, fromType, toType, n := shape.EdgeType, shape.FromType, shape.ToType, shape.Count
		e, declared := s.relationEnds[strings.ToLower(strings.TrimSpace(edgeType))]
		if !declared {
			continue
		}
		// An edge with a foot outside the prose vocabulary is not this
		// vocabulary's edge, whatever it is spelled.
		//
		// A knowledge base holds two vocabularies with one namespace between
		// them, and they collide: `exports` means "a Node exports a Gateway" in
		// the prose the extractor reads and "a file exports a function" in the
		// code graph the importer writes. Judging the second against the first
		// reported 452 imported edges as misdirected on a live SDS base — real
		// data, correctly imported, filed as a defect, and enough of it to bury
		// the eleven findings underneath that were true.
		//
		// Both ends must be declared, not either: a `Node → function` edge has
		// one foot in each world, and no declared end can say anything about
		// where the other foot belongs.
		if !s.declaresNodeType(fromType) || !s.declaresNodeType(toType) {
			continue
		}
		if containsFold(e.From, fromType) && containsFold(e.To, toType) {
			continue
		}
		t := byRelation[e.Name]
		if t == nil {
			t = &tally{
				want:   summarizeEnd(e.From) + " → " + summarizeEnd(e.To),
				shapes: map[string]int{},
			}
			byRelation[e.Name] = t
		}
		t.count += n
		t.shapes[fromType+" → "+toType] += n
		if containsFold(e.To, fromType) && containsFold(e.From, toType) {
			t.reversed += n
		}
	}

	out := make([]MisdirectedEdge, 0, len(byRelation))
	for name, t := range byRelation {
		got := commonest(t.shapes)
		out = append(out, MisdirectedEdge{
			Relation: name, Want: t.want, Got: got, GotCount: t.shapes[got],
			Count: t.count, Reversed: t.reversed,
		})
	}
	// Worst first, then by name so two relations equally wrong do not swap
	// places between runs.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Relation < out[j].Relation
	})
	return out, nil
}

// summarizeEnd renders a declared end for a one-line report. A wide end — SDS
// declares seven source types for `has_state` — would otherwise push the shape
// that is actually wrong off the end of the line, which is the part a reader
// came for.
func summarizeEnd(types []string) string {
	const show = 3
	if len(types) <= show {
		return strings.Join(types, "|")
	}
	return strings.Join(types[:show], "|") + fmt.Sprintf("|+%d more", len(types)-show)
}

// containsFold reports whether a declared end admits this node type, compared
// case-insensitively for the reason every other comparison over this vocabulary
// is: the type in the graph is whatever the extracting model emitted.
func containsFold(types []string, t string) bool {
	for _, declared := range types {
		if strings.EqualFold(declared, t) {
			return true
		}
	}
	return false
}

// commonest returns the most frequent key, ties broken by name.
func commonest(counts map[string]int) string {
	best, bestN := "", -1
	for k, n := range counts {
		if n > bestN || (n == bestN && k < best) {
			best, bestN = k, n
		}
	}
	return best
}

// namedTypeCounts drops the untyped bucket from one of the graph's censuses.
//
// The library counts untyped rows under the empty string rather than omitting
// them, so that a caller summing the census gets the node total back and "there
// are 400 nodes nobody typed" stays visible. This caller is comparing against a
// declared vocabulary, where a blank is not a type the domain forgot to
// declare: left in, it would be reported as an invented type spelled "".
func namedTypeCounts(counts map[string]int, err error) (map[string]int, error) {
	if err != nil {
		return nil, fmt.Errorf("count types: %w", err)
	}
	out := make(map[string]int, len(counts))
	for name, n := range counts {
		if name = strings.TrimSpace(name); name != "" {
			out[name] = n
		}
	}
	return out, nil
}

// diffVocabulary splits what is in the graph against what was declared.
//
// Comparison is case-insensitive for the same reason WithRelationTypes widens
// case before handing types to traversal: the type in the graph is whatever the
// extracting model emitted, and "Contains" is the declared "contains" with a
// capital letter, not a type somebody invented.
func diffVocabulary(declared []string, found map[string]int) (undeclared map[string]int, unused []string) {
	known := make(map[string]string, len(declared))
	for _, t := range declared {
		known[strings.ToLower(t)] = t
	}
	seen := make(map[string]bool, len(declared))

	for t, n := range found {
		if canonical, ok := known[strings.ToLower(t)]; ok {
			seen[canonical] = true
			continue
		}
		if undeclared == nil {
			undeclared = make(map[string]int)
		}
		undeclared[t] = n
	}
	for _, t := range declared {
		if !seen[t] {
			unused = append(unused, t)
		}
	}
	sort.Strings(unused)
	return undeclared, unused
}

// The types cortexdb writes on its own behalf. It exports no constants for
// them; these are the spellings in its GraphRAG ingest path.
var (
	cortexdbOwnNodeTypes = []string{"chunk"}
	cortexdbOwnEdgeTypes = []string{"mentions"}
)

// partitionImported splits the types the prose vocabulary did not claim into
// the ones the imported vocabulary declares and the ones nobody did. Exact
// match, for the reason WithImportedVocabulary gives. Either result is nil
// when empty, so a caller comparing against "no drift" sees the same shape
// diffVocabulary produces.
func partitionImported(undeclared map[string]int, imported []string) (stillUndeclared, importedFound map[string]int) {
	if len(undeclared) == 0 || len(imported) == 0 {
		return undeclared, nil
	}
	known := make(map[string]struct{}, len(imported))
	for _, t := range imported {
		known[t] = struct{}{}
	}
	for t, n := range undeclared {
		if _, ok := known[t]; ok {
			if importedFound == nil {
				importedFound = make(map[string]int)
			}
			importedFound[t] = n
			continue
		}
		if stillUndeclared == nil {
			stillUndeclared = make(map[string]int)
		}
		stillUndeclared[t] = n
	}
	return stillUndeclared, importedFound
}

// keepDeclaredTypes stops an unspecified mention from demoting a declared type.
//
// Entity nodes upsert with ON CONFLICT ... SET node_type = excluded.node_type,
// so the last write wins on type. An extractor that recognises a name without
// placing it emits no type, and vocabulary mode canonicalises the blank onto
// the open Entity supertype — a type this package declares itself, so nothing
// downstream rejects it. The node keeps its content and edges and loses the one
// thing the vocabulary is read for.
//
// cortexdb has its own guard for this, but it fires on the literal "entity" an
// extractor writes, before canonicalisation has folded the blank onto a
// declared name. That "Entity" means "unspecified" here is this package's
// convention, so this is where it has to be honoured.
//
// A mention that names a type is left alone even when the vocabulary never
// declared it: that is an assertion, drift reports it, and overriding it here
// would hide that the model disagreed.
//
// Best-effort. A lookup that fails leaves the batch as it came, for the same
// reason the callers tolerate a failed write: losing a type is bad, refusing to
// ingest is worse.
func (s *Store) keepDeclaredTypes(ctx context.Context, ents []cortexdb.ToolEntityInput) {
	if len(s.entityTypes) == 0 {
		return
	}
	unspecified := make(map[string][]int)
	for i, e := range ents {
		t := strings.TrimSpace(e.Type)
		if t != "" && !strings.EqualFold(t, entityInterface) {
			continue
		}
		id := strings.TrimSpace(e.ID)
		if id == "" {
			id = cortexdb.EntityNodeID(e.Name)
		}
		unspecified[id] = append(unspecified[id], i)
	}
	if len(unspecified) == 0 {
		return
	}

	ids := make([]string, 0, len(unspecified))
	for id := range unspecified {
		ids = append(ids, id)
	}
	nodes, err := s.db.Graph().GetNodesBatch(ctx, ids)
	if err != nil {
		log.Printf("[opsdoctor] knowledge: checking declared types failed, an unspecified mention may demote one: %v", err)
		return
	}
	for _, n := range nodes {
		if n == nil || !s.declaresNodeType(n.NodeType) {
			continue
		}
		for _, i := range unspecified[n.ID] {
			ents[i].Type = n.NodeType
		}
	}
}

// misdirectedNodes finds which nodes the misdirected edges run through.
//
// misdirectedEdges groups by type shape, which answers "how is this relation
// wrong" and not "what is wrong". On a live base eighteen non-conforming edges
// came from thirteen nodes, and two of those carried six between them: one name
// the graph typed as a command that the source calls a flag, and one correctly
// typed thing that simply is not a configuration parameter. Six edges, two
// places to look.
//
// A node carrying a single edge is not reported: that list is the same list of
// edges in another shape, and for one edge either endpoint "explains" it, which
// is not a finding.
func (s *Store) misdirectedNodes(ctx context.Context) ([]MisdirectedNode, error) {
	if len(s.relationEnds) == 0 {
		return nil, nil
	}
	// Unfiltered for the same reason as misdirectedEdges: the library matches
	// edge types exactly and this vocabulary is matched by fold.
	pairs, err := s.db.Graph().EdgeEndpointPairs(ctx)
	if err != nil {
		return nil, fmt.Errorf("find misdirected nodes: %w", err)
	}

	type offender struct {
		id, name, nodeType string
		edges              int
	}
	byNode := make(map[string]*offender)
	note := func(id, name, nodeType string, n int) {
		o := byNode[id]
		if o == nil {
			o = &offender{id: id, name: name, nodeType: nodeType}
			byNode[id] = o
		}
		o.edges += n
	}

	for _, p := range pairs {
		fromID, fromType, fromName := p.From.ID, p.From.NodeType, p.From.Content
		toID, toType, toName := p.To.ID, p.To.NodeType, p.To.Content
		n := p.Count
		end, declared := s.relationEnds[strings.ToLower(strings.TrimSpace(p.EdgeType))]
		if !declared {
			continue
		}
		// Same guard as misdirectedEdges: an edge with a foot outside the prose
		// vocabulary belongs to the code graph, which shares this namespace and
		// not this vocabulary.
		if !s.declaresNodeType(fromType) || !s.declaresNodeType(toType) {
			continue
		}
		okFrom, okTo := containsFold(end.From, fromType), containsFold(end.To, toType)
		switch {
		case okFrom && okTo:
			// Conforming.
		case okFrom:
			note(toID, toName, toType, n)
		case okTo:
			note(fromID, fromName, fromType, n)
		default:
			// Both ends wrong says nothing about either one.
		}
	}

	out := make([]MisdirectedNode, 0, len(byNode))
	for _, o := range byNode {
		if o.edges < 2 {
			continue
		}
		out = append(out, MisdirectedNode{ID: o.id, Name: o.name, Type: o.nodeType, Edges: o.edges})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Edges != out[j].Edges {
			return out[i].Edges > out[j].Edges
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
