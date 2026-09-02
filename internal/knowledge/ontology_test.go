package knowledge

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	cortexdb "github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// openStore opens a throwaway knowledge base. The embedder address is
// unroutable on purpose: everything these tests exercise is SQL and ontology
// bookkeeping, and a test that could reach an embedder would depend on one.
func openStore(t *testing.T, opts ...Option) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "k.db"), "http://127.0.0.1:1/v1", "x", "none", 8, opts...)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// A live SDS knowledge base carried a "StorageClass" node type the domain never
// declared — invented by the extracting model, stored without complaint, and
// only found by running a GROUP BY by hand. Extraction is done by a language
// model, so this is not preventable; it only has to be visible.
func TestDiffVocabularyFindsInventedTypes(t *testing.T) {
	declared := []string{"Node", "DRBDResource", "Gateway"}
	found := map[string]int{"Node": 25, "DRBDResource": 20, "StorageClass": 3}

	undeclared, unused := diffVocabulary(declared, found)

	if want := map[string]int{"StorageClass": 3}; !reflect.DeepEqual(undeclared, want) {
		t.Errorf("undeclared = %v, want %v", undeclared, want)
	}
	if want := []string{"Gateway"}; !reflect.DeepEqual(unused, want) {
		t.Errorf("unused = %v, want %v", unused, want)
	}
}

// "Contains" is the declared "contains" with a capital letter, not a type
// somebody invented. Reporting it as drift would bury the real thing under
// noise the extractor produces on every run.
func TestDiffVocabularyIgnoresCasing(t *testing.T) {
	undeclared, unused := diffVocabulary(
		[]string{"contains", "depends_on"},
		map[string]int{"Contains": 4, "DEPENDS_ON": 2},
	)
	if len(undeclared) != 0 {
		t.Errorf("undeclared = %v, want none", undeclared)
	}
	if len(unused) != 0 {
		t.Errorf("unused = %v, want none — both were extracted, just cased differently", unused)
	}
}

func TestDiffVocabularyOnAnEmptyGraph(t *testing.T) {
	undeclared, unused := diffVocabulary([]string{"Node", "Gateway"}, map[string]int{})
	if len(undeclared) != 0 {
		t.Errorf("undeclared = %v, want none", undeclared)
	}
	if want := []string{"Gateway", "Node"}; !reflect.DeepEqual(unused, want) {
		t.Errorf("unused = %v, want %v", unused, want)
	}
}

// The fingerprint identifies a vocabulary, not a spelling of it. Reordering
// domain.toml must not read as a change of meaning, or every reorder looks like
// the graph needs re-extracting.
func TestVocabularyFingerprintIgnoresOrder(t *testing.T) {
	a := &Store{entityTypes: []string{"Node", "Gateway"}, relationTypes: []string{"contains", "backs"}}
	b := &Store{entityTypes: []string{"Gateway", "Node"}, relationTypes: []string{"backs", "contains"}}
	if a.vocabularyFingerprint() != b.vocabularyFingerprint() {
		t.Error("reordering the same vocabulary changed its fingerprint")
	}
}

// Changing the vocabulary must change the fingerprint, which is the whole point:
// a graph extracted under the old one may hold edges the new one never walks.
func TestVocabularyFingerprintTracksContent(t *testing.T) {
	a := &Store{entityTypes: []string{"Node"}, relationTypes: []string{"contains"}}
	b := &Store{entityTypes: []string{"Node"}, relationTypes: []string{"CONTAINS_THING"}}
	if a.vocabularyFingerprint() == b.vocabularyFingerprint() {
		t.Error("a different vocabulary produced the same fingerprint")
	}
}

// Entity and relation vocabularies must not be able to swap without notice.
func TestVocabularyFingerprintSeparatesEntitiesFromRelations(t *testing.T) {
	a := &Store{entityTypes: []string{"x"}, relationTypes: []string{"y"}}
	b := &Store{entityTypes: []string{"y"}, relationTypes: []string{"x"}}
	if a.vocabularyFingerprint() == b.vocabularyFingerprint() {
		t.Error("swapping entity and relation vocabularies produced the same fingerprint")
	}
}

// A domain that declares no vocabulary must not be reported as having drifted
// from one.
func TestDriftIsSilentWithoutADeclaredVocabulary(t *testing.T) {
	d := OntologyDrift{}
	if d.Registered {
		t.Error("an unregistered ontology must not claim to be registered")
	}
	if !d.Clean() {
		t.Error("no declared vocabulary means nothing to drift from")
	}
}

func TestDriftCleanOnlyWhenNothingWasInvented(t *testing.T) {
	dirty := OntologyDrift{Registered: true, UndeclaredEdgeTypes: map[string]int{"DEPLOYED_ON": 12}}
	if dirty.Clean() {
		t.Error("an undeclared edge type is drift: it is stored and never traversed")
	}
	// Declaring a type the corpus never mentions is untidy, not broken.
	tidy := OntologyDrift{Registered: true, UnusedNodeTypes: []string{"WANTunnel"}}
	if !tidy.Clean() {
		t.Error("a declared-but-unused type is not drift")
	}
}

func TestWithOntologyTrimsAndDedupes(t *testing.T) {
	s := &Store{}
	WithOntology(" SDS ", []string{"Node", " Node ", "", "Gateway"}, []string{"contains", "contains"})(s)

	if s.ontologyName != "SDS" {
		t.Errorf("name = %q, want %q", s.ontologyName, "SDS")
	}
	if want := []string{"Node", "Gateway"}; !reflect.DeepEqual(s.entityTypes, want) {
		t.Errorf("entityTypes = %v, want %v", s.entityTypes, want)
	}
	if want := []string{"contains"}; !reflect.DeepEqual(s.relationTypes, want) {
		t.Errorf("relationTypes = %v, want %v", s.relationTypes, want)
	}
}

// Registering the ontology must never stop the graph from being written to.
//
// It did. An ACTIVE cortexdb schema is validated on every entity upsert, and
// the schema declares a primary key property the LLM extractor does not emit,
// so every entity in every document was refused with "missing primary key
// property id". Ingest reported success throughout — IngestSemantic discarded
// both the extraction error and the upsert error — and a live knowledge base
// took 264 new chunks without gaining a single graph node.
//
// The registration exists to record the vocabulary and give DriftReport a
// baseline. That needs the schema stored, not enforced.
func TestRegisteringTheOntologyDoesNotBlockEntityWrites(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "k.db"), "http://127.0.0.1:1/v1", "x", "none", 8,
		WithOntology("test", []string{"Gateway", "Node"}, []string{"contains", "backs"}))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()

	if _, err := s.tb.UpsertEntities(context.Background(), cortexdb.ToolUpsertEntitiesRequest{
		DocumentID: "d1",
		Entities: []cortexdb.ToolEntityInput{
			{Name: "nfs-gw", Type: "Gateway", Description: "an extracted entity, with no id property"},
		},
	}); err != nil {
		t.Fatalf("upsert refused after registering the ontology: %v", err)
	}
}

// The schema must be stored AND active — but only in vocabulary mode. Strict
// activation froze every graph write on a live deployment (an LLM extractor
// cannot supply primary keys); registering it deactivated was the workaround,
// which kept ingest alive but gave up canonical spellings and interface
// retrieval. Vocabulary enforcement is what lets both hold at once, and the
// first test above — a keyless upsert succeeding with the schema registered —
// is the proof it does not gate.
func TestOntologyIsActiveAsVocabulary(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "k.db"), "http://127.0.0.1:1/v1", "x", "none", 8,
		WithOntology("test", []string{"Gateway"}, []string{"contains"}))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()

	got, err := s.db.GetOntologySchema(context.Background(), cortexdb.OntologyGetRequest{SchemaID: ontologySchemaID})
	if err != nil {
		t.Fatalf("the schema must be retrievable: %v", err)
	}
	if !got.Schema.Active {
		t.Error("the schema must be active: inactive, it canonicalizes nothing and expands no interfaces")
	}
	if got.Schema.Enforcement != cortexdb.OntologyEnforcementVocabulary {
		t.Errorf("enforcement = %q, want vocabulary — strict enforcement refuses every LLM-extracted entity", got.Schema.Enforcement)
	}
	if got.Schema.Metadata["vocabulary_fingerprint"] == "" {
		t.Error("the fingerprint is what makes a graph traceable to a vocabulary")
	}
}

// Registering object types recorded the vocabulary but could not be asked
// anything with it. Declaring one interface they all implement is what turns the
// record into a query: cortexdb expands an interface to its implementors
// wherever a node type filter is accepted, so "the active domain's entity types"
// becomes something retrieval can name.
func TestDeclaredTypesImplementOneInterfaceRetrievalCanAskFor(t *testing.T) {
	s := openStore(t, WithOntology("test", []string{"Gateway", "StoragePool"}, []string{"backs"}))

	got, err := s.db.GetOntologySchema(context.Background(), cortexdb.OntologyGetRequest{SchemaID: ontologySchemaID})
	if err != nil {
		t.Fatalf("get schema: %v", err)
	}

	var declared bool
	for _, it := range got.Schema.InterfaceTypes {
		if it.APIName == domainEntityInterface {
			declared = true
		}
	}
	if !declared {
		t.Fatalf("interface types = %v, want one named %q", got.Schema.InterfaceTypes, domainEntityInterface)
	}
	for _, ot := range got.Schema.ObjectTypes {
		if !slices.Contains(ot.Implements, domainEntityInterface) {
			t.Errorf("object type %q implements %v — a type outside the interface is invisible to a filter that asks for it",
				ot.APIName, ot.Implements)
		}
	}
}

// The explorer's seed query used to ORDER BY a literal list of twelve LINSTOR
// type names. A domain declaring anything outside that list — which is every
// domain but the one it was transcribed from, and by now not even that one,
// since SDS renamed Resource to DRBDResource and dropped Cluster — sorted its
// own entities behind imported code. "Gateway" is one such type: declared here,
// absent from that list.
func TestNameSeedsPreferWhateverTheDomainDeclared(t *testing.T) {
	s := openStore(t, WithOntology("test", []string{"Gateway"}, nil))
	// Named so the undeclared node is the SHORTER of the two: FindNodes breaks a
	// tie by length, so a test where the declared node also happens to be
	// shorter would pass without the interface filter doing anything.
	upsert(t, s, cortexdb.ToolEntityInput{Name: "gw.go", Type: "file"})
	upsert(t, s, cortexdb.ToolEntityInput{Name: "nfs-gw-a", Type: "Gateway"})

	got := s.seedsByName(context.Background(), "gw", 10)

	if len(got) != 2 {
		t.Fatalf("seeds = %v, want both nodes — the explorer must still reach imported code", got)
	}
	if typ := nodeTypeOf(t, s, got[0]); typ != "Gateway" {
		t.Errorf("first seed is a %q; the domain's own declared type must lead", typ)
	}
}

func upsert(t *testing.T, s *Store, ents ...cortexdb.ToolEntityInput) {
	t.Helper()
	if _, err := s.tb.UpsertEntities(context.Background(), cortexdb.ToolUpsertEntitiesRequest{
		DocumentID: "d1", Entities: ents,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
}

func nodeTypeOf(t *testing.T, s *Store, id string) string {
	t.Helper()
	var typ string
	if err := s.db.SQL().QueryRowContext(context.Background(),
		`SELECT node_type FROM graph_nodes WHERE id = ?`, id).Scan(&typ); err != nil {
		t.Fatalf("read node %q: %v", id, err)
	}
	return typ
}

// A domain that declares no vocabulary registers no schema, so there is no
// interface to expand. Seeding must still work — it is how the explorer finds
// anything at all.
func TestNameSeedsWorkWithoutADeclaredVocabulary(t *testing.T) {
	s := openStore(t)
	upsert(t, s, cortexdb.ToolEntityInput{Name: "gw.go", Type: "file"})

	if got := s.seedsByName(context.Background(), "gw", 10); len(got) != 1 {
		t.Errorf("seeds = %v, want the one node whose name matches", got)
	}
}

// A property predicate reads a node's properties JSON, and the only keys
// cortexdb writes there for an extracted entity are name and description. A
// "content" property was declared here for a while: the text lives in the
// graph_nodes.content COLUMN, so every filter over it selected the empty set
// while looking perfectly well-formed.
func TestDeclaredPropertiesAreOnesANodeActuallyCarries(t *testing.T) {
	s := openStore(t, WithOntology("test", []string{"Gateway"}, nil))

	if _, err := s.tb.UpsertEntities(context.Background(), cortexdb.ToolUpsertEntitiesRequest{
		DocumentID: "d1",
		Entities:   []cortexdb.ToolEntityInput{{Name: "nfs-gw", Type: "Gateway", Description: "exports NFS"}},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.db.GetOntologySchema(context.Background(), cortexdb.OntologyGetRequest{SchemaID: ontologySchemaID})
	if err != nil {
		t.Fatalf("get schema: %v", err)
	}
	for _, ot := range got.Schema.ObjectTypes {
		if ot.APIName != "Gateway" {
			continue
		}
		for _, p := range ot.Properties {
			if p.APIName == "id" { // the primary key, a column by necessity
				continue
			}
			ids, err := s.db.ResolveObjectSet(context.Background(), cortexdb.ObjectSet{
				Kind:   cortexdb.ObjectSetFilter,
				Source: &cortexdb.ObjectSet{Kind: cortexdb.ObjectSetInterfaceBase, InterfaceType: domainEntityInterface},
				Where: &cortexdb.ObjectSetPredicate{
					Op: cortexdb.PredicateIsNull, Property: p.APIName,
				},
			})
			if err != nil {
				t.Fatalf("resolve %q: %v", p.APIName, err)
			}
			if len(ids) != 0 {
				t.Errorf("property %q is null on %d upserted entities — it is declared but not written, so no predicate over it can match", p.APIName, len(ids))
			}
		}
	}
}

// cortexdb derives a schema version on every save, and every command that opens
// the store registers. Rewriting an identical vocabulary made the version
// counter measure how many times someone ran `search`, and made read-only
// commands write to the database to learn nothing.
func TestReopeningWithTheSameVocabularyDoesNotRewriteTheSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "k.db")
	opts := WithOntology("test", []string{"Gateway"}, []string{"backs"})

	version := func() int {
		s, err := Open(path, "http://127.0.0.1:1/v1", "x", "none", 8, opts)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer func() { _ = s.Close() }()
		got, err := s.db.GetOntologySchema(context.Background(), cortexdb.OntologyGetRequest{SchemaID: ontologySchemaID})
		if err != nil {
			t.Fatalf("get schema: %v", err)
		}
		return got.Schema.Version
	}

	first := version()
	if second := version(); second != first {
		t.Errorf("version went %d → %d on reopening with an unchanged vocabulary", first, second)
	}
}

// A vocabulary that DID change must be rewritten, or the stored fingerprint goes
// on describing a domain.toml nobody is running and drift reports the wrong
// baseline.
func TestChangingTheVocabularyRewritesTheSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.db")

	fingerprint := func(opt Option) string {
		s, err := Open(path, "http://127.0.0.1:1/v1", "x", "none", 8, opt)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer func() { _ = s.Close() }()
		got, err := s.db.GetOntologySchema(context.Background(), cortexdb.OntologyGetRequest{SchemaID: ontologySchemaID})
		if err != nil {
			t.Fatalf("get schema: %v", err)
		}
		return got.Schema.Metadata["vocabulary_fingerprint"]
	}

	before := fingerprint(WithOntology("test", []string{"Gateway"}, []string{"backs"}))
	after := fingerprint(WithOntology("test", []string{"Gateway", "StoragePool"}, []string{"backs"}))
	if before == after {
		t.Error("the stored fingerprint still describes the previous vocabulary")
	}
}

// A graph extracted under one vocabulary and queried under another holds edges
// spelled the old way; expansion filters every one of them out and GraphRAG
// quietly becomes plain vector search.
//
// The comparison used to be against the schema's REGISTERED fingerprint, which
// registration overwrites at Open — so it compared the live domain.toml with
// itself and could not fire. What the graph was actually built under is only
// knowable at ingest, which is where it is now recorded.
func TestDriftNoticesAGraphExtractedUnderAnEarlierVocabulary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.db")

	before := openAt(t, path, WithOntology("test", []string{"Gateway"}, []string{"backs"}))
	ingestEntities(t, before, cortexdb.ToolEntityInput{Name: "nfs-gw", Type: "Gateway"})
	_ = before.Close()

	after := openAt(t, path, WithOntology("test", []string{"Gateway", "StoragePool"}, []string{"backs"}))
	d, err := after.DriftReport(context.Background())
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if d.StoredFingerprint == "" {
		t.Fatal("nothing recorded what the graph was extracted under")
	}
	if d.StoredFingerprint == d.Fingerprint {
		t.Error("the graph was extracted under the previous vocabulary and drift did not say so")
	}
}

// Re-ingesting under an unchanged vocabulary is the normal case and must stay
// silent, or the warning is noise on every run and gets ignored on the run that
// matters.
func TestDriftIsSilentWhenTheVocabularyDidNotChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.db")
	opt := WithOntology("test", []string{"Gateway"}, []string{"backs"})

	first := openAt(t, path, opt)
	ingestEntities(t, first, cortexdb.ToolEntityInput{Name: "nfs-gw", Type: "Gateway"})
	_ = first.Close()

	second := openAt(t, path, opt)
	d, err := second.DriftReport(context.Background())
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if d.StoredFingerprint != d.Fingerprint {
		t.Errorf("stored %q vs loaded %q — the vocabulary never changed", d.StoredFingerprint, d.Fingerprint)
	}
}

// A store nothing has been ingested into has no extraction to compare against.
// Reporting that as drift would flag every freshly built index.
func TestDriftHasNoExtractionRecordBeforeAnyIngest(t *testing.T) {
	s := openStore(t, WithOntology("test", []string{"Gateway"}, []string{"backs"}))
	d, err := s.DriftReport(context.Background())
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if d.StoredFingerprint != "" {
		t.Errorf("stored fingerprint = %q, want empty — nothing has been extracted", d.StoredFingerprint)
	}
}

// openAt opens a store at a specific path, so a test can reopen the same one.
func openAt(t *testing.T, path string, opts ...Option) *Store {
	t.Helper()
	s, err := Open(path, "http://127.0.0.1:1/v1", "x", "none", 8, opts...)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

// ingestEntities is the graph half of IngestSemantic: the entity write, and the
// record of which vocabulary it happened under. The vector half needs an
// embedder and has nothing to do with the ontology.
func ingestEntities(t *testing.T, s *Store, ents ...cortexdb.ToolEntityInput) {
	t.Helper()
	upsert(t, s, ents...)
	s.noteExtractionVocabulary(context.Background())
}

// A link's two sides are the traversal's two directions: cortexdb keeps only
// nodes of the FAR side's object type, so with the open Entity supertype on
// both sides every ends-aware traversal kept only nodes whose type is literally
// "Entity" and returned nothing, while the schema read as complete.
func TestDeclaredEndsAreRegisteredAsTheLinksSides(t *testing.T) {
	s := openStore(t,
		WithOntology("test", []string{"StoragePool", "DRBDResource"}, []string{"backs", "contains"}),
		WithRelationEnds([]RelationEnd{{Name: "backs", From: []string{"StoragePool"}, To: []string{"DRBDResource"}}}))

	got, err := s.db.GetOntologySchema(context.Background(), cortexdb.OntologyGetRequest{SchemaID: ontologySchemaID})
	if err != nil {
		t.Fatalf("get schema: %v", err)
	}

	var backs, contains *cortexdb.OntologyLinkType
	for i := range got.Schema.LinkTypes {
		switch got.Schema.LinkTypes[i].APIName {
		case "backs":
			backs = &got.Schema.LinkTypes[i]
		case "contains":
			contains = &got.Schema.LinkTypes[i]
		}
	}
	if backs == nil || contains == nil {
		t.Fatalf("link types = %+v, want both", got.Schema.LinkTypes)
	}
	if backs.A.ObjectTypeAPIName != "StoragePool" || backs.B.ObjectTypeAPIName != "DRBDResource" {
		t.Errorf("backs runs %s → %s, want StoragePool → DRBDResource",
			backs.A.ObjectTypeAPIName, backs.B.ObjectTypeAPIName)
	}
	// A relation that declared nothing keeps the open supertype: a traversal
	// that reaches nothing is the honest encoding of "the domain said nothing".
	if contains.A.ObjectTypeAPIName != entityInterface {
		t.Errorf("contains declared no ends but its A side is %q", contains.A.ObjectTypeAPIName)
	}
}

// A traversal by link SIDE is what the ends buy: "backs" and "backs_of" must
// mean opposite things. With the supertype on both sides this returned the empty
// set in both directions.
func TestDeclaredEndsMakeADirectionalTraversalWork(t *testing.T) {
	s := openStore(t,
		WithOntology("test", []string{"StoragePool", "DRBDResource"}, []string{"backs"}),
		WithRelationEnds([]RelationEnd{{Name: "backs", From: []string{"StoragePool"}, To: []string{"DRBDResource"}}}))
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "tank", Type: "StoragePool"},
		cortexdb.ToolEntityInput{Name: "r0", Type: "DRBDResource"})
	relate(t, s, "tank", "r0", "backs")

	pools, err := s.db.ResolveObjectSet(context.Background(), cortexdb.ObjectSet{
		Kind: cortexdb.ObjectSetBase, ObjectType: "StoragePool",
	})
	if err != nil {
		t.Fatalf("resolve pools: %v", err)
	}
	ids := make([]string, 0, len(pools))
	for id := range pools {
		ids = append(ids, id)
	}

	forward, err := s.db.ResolveObjectSet(context.Background(), cortexdb.ObjectSet{
		Kind:   cortexdb.ObjectSetSearchAround,
		Source: &cortexdb.ObjectSet{Kind: cortexdb.ObjectSetStatic, ObjectIDs: ids},
		Link:   "backs",
	})
	if err != nil {
		t.Fatalf("search around: %v", err)
	}
	if len(forward) != 1 {
		t.Errorf("walking backs from the pool reached %d nodes, want the one resource", len(forward))
	}

	// And the direction has to be real, not just non-empty. Walking the same
	// side from the resource end reaches the pool, which is not the far side's
	// type, so it must select nothing — that is the whole difference between a
	// link side and a bare edge type filter.
	backward, err := s.db.ResolveObjectSet(context.Background(), cortexdb.ObjectSet{
		Kind:   cortexdb.ObjectSetSearchAround,
		Source: &cortexdb.ObjectSet{Kind: cortexdb.ObjectSetBase, ObjectType: "DRBDResource"},
		Link:   "backs",
	})
	if err != nil {
		t.Fatalf("search around: %v", err)
	}
	if len(backward) != 0 {
		t.Errorf("walking backs from the resource reached %d nodes, want none — backs runs the other way", len(backward))
	}
}

// The ends are part of the vocabulary. Leaving them out of the fingerprint means
// changing what may sit on either end does not change it — so the registration
// short-circuit decides the schema is already current, the new ends are never
// written, and drift goes on reporting a graph as matching a vocabulary it does
// not.
func TestChangingARelationsEndsChangesTheFingerprint(t *testing.T) {
	base := func(opts ...Option) string {
		s := &Store{}
		WithOntology("t", []string{"A", "B"}, []string{"backs"})(s)
		for _, o := range opts {
			o(s)
		}
		return s.vocabularyFingerprint()
	}
	a := base(WithRelationEnds([]RelationEnd{{Name: "backs", From: []string{"A"}, To: []string{"B"}}}))
	b := base(WithRelationEnds([]RelationEnd{{Name: "backs", From: []string{"B"}, To: []string{"A"}}}))
	if a == b {
		t.Error("reversing a relation's ends produced the same fingerprint")
	}
	if none := base(); none == a {
		t.Error("declaring ends at all produced the same fingerprint as declaring none")
	}
}

// A `backs` asserted from the resource to the pool is stored without complaint,
// reads as a fact, and is walked as one. Nothing in the answer it produces says
// the arrow points the wrong way — the declaration is the only thing that can
// disagree with it.
func TestDriftFindsRelationsAssertedBackwards(t *testing.T) {
	s := openStore(t,
		WithOntology("test", []string{"StoragePool", "DRBDResource"}, []string{"backs"}),
		WithRelationEnds([]RelationEnd{{Name: "backs", From: []string{"StoragePool"}, To: []string{"DRBDResource"}}}))
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "tank", Type: "StoragePool"},
		cortexdb.ToolEntityInput{Name: "r0", Type: "DRBDResource"})
	relate(t, s, "r0", "tank", "backs") // backwards

	d, err := s.DriftReport(context.Background())
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if len(d.MisdirectedEdges) != 1 {
		t.Fatalf("misdirected = %+v, want the one backwards relation", d.MisdirectedEdges)
	}
	m := d.MisdirectedEdges[0]
	if m.Relation != "backs" || m.Reversed != 1 || m.Count != 1 {
		t.Errorf("misdirected = %+v, want backs, 1 edge, 1 of them exactly reversed", m)
	}
	if d.Clean() {
		t.Error("a relation pointing the wrong way is not a clean graph")
	}
}

// An edge the right way round must not be reported, or the check is noise on
// every healthy graph and gets ignored on the one that is not.
func TestDriftIsSilentWhenTheEndsMatch(t *testing.T) {
	s := openStore(t,
		WithOntology("test", []string{"StoragePool", "DRBDResource"}, []string{"backs"}),
		WithRelationEnds([]RelationEnd{{Name: "backs", From: []string{"StoragePool"}, To: []string{"DRBDResource"}}}))
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "tank", Type: "StoragePool"},
		cortexdb.ToolEntityInput{Name: "r0", Type: "DRBDResource"})
	relate(t, s, "tank", "r0", "backs")

	d, err := s.DriftReport(context.Background())
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if len(d.MisdirectedEdges) != 0 {
		t.Errorf("misdirected = %+v, want none", d.MisdirectedEdges)
	}
}

// A relation that declared no ends has nothing to contradict. Reporting its
// edges as misdirected would make declaring ends for one relation flag every
// other one.
func TestARelationWithoutDeclaredEndsIsNeverMisdirected(t *testing.T) {
	s := openStore(t,
		WithOntology("test", []string{"StoragePool", "DRBDResource"}, []string{"backs", "related"}),
		WithRelationEnds([]RelationEnd{{Name: "backs", From: []string{"StoragePool"}, To: []string{"DRBDResource"}}}))
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "tank", Type: "StoragePool"},
		cortexdb.ToolEntityInput{Name: "r0", Type: "DRBDResource"})
	relate(t, s, "r0", "tank", "related") // whichever way round, nothing declared it

	d, err := s.DriftReport(context.Background())
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if len(d.MisdirectedEdges) != 0 {
		t.Errorf("misdirected = %+v, want none", d.MisdirectedEdges)
	}
}

// relate writes one edge between two named entities.
func relate(t *testing.T, s *Store, from, to, edgeType string) {
	t.Helper()
	if _, err := s.tb.UpsertRelations(context.Background(), cortexdb.ToolUpsertRelationsRequest{
		DocumentID: "d1",
		Relations:  []cortexdb.ToolRelationInput{{From: from, To: to, Type: edgeType}},
	}); err != nil {
		t.Fatalf("upsert relation: %v", err)
	}
}

// A relation polymorphic in one end is the common case in an ops vocabulary — a
// Snapshot and a Backup both protect a Volume — and forcing it to a single type
// would mean declaring something false or leaving the relation without ends at
// all. cortexdb models it as an interface on the link side; the domain just
// lists the types.
func TestAPolymorphicEndAdmitsEveryTypeItLists(t *testing.T) {
	s := openStore(t,
		WithOntology("test", []string{"Snapshot", "Backup", "Volume"}, []string{"protects"}),
		WithRelationEnds([]RelationEnd{{
			Name: "protects", From: []string{"Snapshot", "Backup"}, To: []string{"Volume"},
		}}))
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "snap-1", Type: "Snapshot"},
		cortexdb.ToolEntityInput{Name: "backup-1", Type: "Backup"},
		cortexdb.ToolEntityInput{Name: "vol-1", Type: "Volume"})
	relate(t, s, "snap-1", "vol-1", "protects")
	relate(t, s, "backup-1", "vol-1", "protects")

	d, err := s.DriftReport(context.Background())
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if len(d.MisdirectedEdges) != 0 {
		t.Errorf("misdirected = %+v, want none — both types are declared sources", d.MisdirectedEdges)
	}
}

// The set is still a constraint, not a shrug: a type the end does not list is
// as wrong as a reversed edge.
func TestAPolymorphicEndStillRejectsATypeItDoesNotList(t *testing.T) {
	s := openStore(t,
		WithOntology("test", []string{"Snapshot", "Backup", "Volume"}, []string{"protects"}),
		WithRelationEnds([]RelationEnd{{
			Name: "protects", From: []string{"Snapshot", "Backup"}, To: []string{"Volume"},
		}}))
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "vol-1", Type: "Volume"},
		cortexdb.ToolEntityInput{Name: "vol-2", Type: "Volume"})
	relate(t, s, "vol-1", "vol-2", "protects")

	d, err := s.DriftReport(context.Background())
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if len(d.MisdirectedEdges) != 1 {
		t.Fatalf("misdirected = %+v, want the Volume->Volume edge", d.MisdirectedEdges)
	}
}

// A polymorphic end is registered as an interface those types implement, and
// the traversal has to arrive at all of them. Registering it and then keeping
// only nodes whose type is the interface's own name — which nothing is stored
// as — is the failure this whole path exists to avoid.
func TestAPolymorphicEndIsTraversableToEveryImplementor(t *testing.T) {
	s := openStore(t,
		WithOntology("test", []string{"Snapshot", "Backup", "Volume"}, []string{"protects"}),
		WithRelationEnds([]RelationEnd{{
			Name: "protects", From: []string{"Snapshot", "Backup"}, To: []string{"Volume"},
		}}))
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "snap-1", Type: "Snapshot"},
		cortexdb.ToolEntityInput{Name: "backup-1", Type: "Backup"},
		cortexdb.ToolEntityInput{Name: "vol-1", Type: "Volume"})
	relate(t, s, "snap-1", "vol-1", "protects")
	relate(t, s, "backup-1", "vol-1", "protects")

	protectors, err := s.db.ResolveObjectSet(context.Background(), cortexdb.ObjectSet{
		Kind:   cortexdb.ObjectSetSearchAround,
		Source: &cortexdb.ObjectSet{Kind: cortexdb.ObjectSetBase, ObjectType: "Volume"},
		Link:   "protects_of",
	})
	if err != nil {
		t.Fatalf("search around: %v", err)
	}
	if len(protectors) != 2 {
		t.Errorf("walking back from the volume reached %d nodes, want the Snapshot and the Backup", len(protectors))
	}
}

// Reordering the types within an end is not a different statement about the
// domain, and treating it as one would make every reorder look like the graph
// needs re-extracting.
func TestReorderingTypesWithinAnEndDoesNotChangeTheFingerprint(t *testing.T) {
	fingerprint := func(from ...string) string {
		s := &Store{}
		WithOntology("t", []string{"Snapshot", "Backup", "Volume"}, []string{"protects"})(s)
		WithRelationEnds([]RelationEnd{{Name: "protects", From: from, To: []string{"Volume"}}})(s)
		return s.vocabularyFingerprint()
	}
	if fingerprint("Snapshot", "Backup") != fingerprint("Backup", "Snapshot") {
		t.Error("reordering the types in one end changed the fingerprint")
	}
	if fingerprint("Snapshot", "Backup") == fingerprint("Snapshot") {
		t.Error("dropping a type from an end produced the same fingerprint")
	}
}

// SDS declares seven source types for has_state. Printed whole they push the
// shape that is actually wrong off the end of the line, which is the part a
// reader came for.
func TestAWideEndIsTrimmedForTheReportLine(t *testing.T) {
	if got := summarizeEnd([]string{"A", "B"}); got != "A|B" {
		t.Errorf("summarizeEnd = %q, want the whole short end", got)
	}
	if got := summarizeEnd([]string{"A", "B", "C", "D", "E"}); got != "A|B|C|+2 more" {
		t.Errorf("summarizeEnd = %q, want the first three and a count", got)
	}
}

// A knowledge base holds two vocabularies with one namespace between them, and
// they collide: `exports` means "a Node exports a Gateway" in the prose the
// extractor reads and "a file exports a function" in the code graph the importer
// writes. Judging the second against the first reported 452 imported edges as
// misdirected on a live SDS base — real data, correctly imported, filed as a
// defect, and enough of it to bury the true findings underneath.
func TestAnImportedCodeEdgeIsNotJudgedAgainstTheProseVocabulary(t *testing.T) {
	s := openStore(t,
		WithOntology("test", []string{"Node", "Gateway"}, []string{"exports"}),
		WithRelationEnds([]RelationEnd{{Name: "exports", From: []string{"Node"}, To: []string{"Gateway"}}}))
	// Neither end is a declared entity type: this is the code graph's `exports`.
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "server.go", Type: "file"},
		cortexdb.ToolEntityInput{Name: "Serve", Type: "function"})
	relate(t, s, "server.go", "Serve", "exports")

	d, err := s.DriftReport(context.Background())
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if len(d.MisdirectedEdges) != 0 {
		t.Errorf("misdirected = %+v, want none — file and function are not this vocabulary's types", d.MisdirectedEdges)
	}
}

// One foot in each world is still not judgeable: no declared end can say where
// the other foot belongs. Requiring BOTH ends to be declared is what makes that
// true rather than half-true.
func TestAnEdgeWithOneFootOutsideTheVocabularyIsNotJudged(t *testing.T) {
	s := openStore(t,
		WithOntology("test", []string{"Node", "Gateway"}, []string{"exports"}),
		WithRelationEnds([]RelationEnd{{Name: "exports", From: []string{"Node"}, To: []string{"Gateway"}}}))
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "node-a", Type: "Node"},
		cortexdb.ToolEntityInput{Name: "Serve", Type: "function"})
	relate(t, s, "node-a", "Serve", "exports")

	d, err := s.DriftReport(context.Background())
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if len(d.MisdirectedEdges) != 0 {
		t.Errorf("misdirected = %+v, want none", d.MisdirectedEdges)
	}
}

// And the filter must not swallow the real thing: both ends declared, neither
// matching the relation's ends, is exactly what the check is for.
func TestAnEdgeBetweenTwoDeclaredTypesIsStillJudged(t *testing.T) {
	s := openStore(t,
		WithOntology("test", []string{"Node", "Gateway", "Package"}, []string{"exports"}),
		WithRelationEnds([]RelationEnd{{Name: "exports", From: []string{"Node"}, To: []string{"Gateway"}}}))
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "drbd-utils", Type: "Package"},
		cortexdb.ToolEntityInput{Name: "nfs-gw", Type: "Gateway"})
	relate(t, s, "drbd-utils", "nfs-gw", "exports")

	d, err := s.DriftReport(context.Background())
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if len(d.MisdirectedEdges) != 1 {
		t.Fatalf("misdirected = %+v, want the Package->Gateway edge", d.MisdirectedEdges)
	}
}

// An unspecified mention must not demote a declared type.
//
// Entity nodes upsert with ON CONFLICT ... SET node_type = excluded.node_type,
// and an extractor that recognises a name without placing it emits no type at
// all. Vocabulary mode then canonicalises the blank onto the open Entity
// supertype — a legitimately declared object type, so nothing rejects it — and
// the StoragePool that had been extracted with a type is gone.
//
// Every consequence the vocabulary buys keys off node_type: drift's
// declaresNodeType stops counting it, the neighbour quota stops giving it
// precedence, and a typed walk's far-end filter stops returning it entirely.
// One chunk that merely mentions "tank" used to remove the pool from every
// ends-aware traversal, silently — which is the failure this package exists to
// make impossible, so pin it rather than meet it as retrieval that got worse.
func TestAnUnspecifiedMentionDoesNotDemoteADeclaredType(t *testing.T) {
	for _, mention := range []string{"", "entity", "Entity", "ENTITY"} {
		t.Run("type="+mention, func(t *testing.T) {
			s := storageStore(t)

			if err := s.UpsertEntities(context.Background(), "d2",
				[]GraphEntity{{Name: "tank", Type: mention}}); err != nil {
				t.Fatalf("mention: %v", err)
			}

			if typ := nodeTypeOf(t, s, "entity:tank"); typ != "StoragePool" {
				t.Fatalf("node type = %q, want the declared StoragePool to survive the mention", typ)
			}

			// The consequence, not just the column: the declaration still buys
			// a typed traversal, which is what the demotion took away.
			got, err := s.Walk(context.Background(), "r0", "backs", WalkIn)
			if err != nil {
				t.Fatalf("walk: %v", err)
			}
			if !got.Typed || len(got.Steps) != 1 || got.Steps[0].Name != "tank" {
				t.Errorf("walk = %+v, want the pool still reachable by its declared end", got)
			}
		})
	}
}

// A mention that names a type is an assertion, not a blank, so it is allowed to
// win even when the vocabulary never declared it. Drift reports the invented
// type; overriding the model here would hide that it disagreed.
func TestAMentionThatNamesATypeStillWins(t *testing.T) {
	s := storageStore(t)

	if err := s.UpsertEntities(context.Background(), "d2",
		[]GraphEntity{{Name: "tank", Type: "Widget"}}); err != nil {
		t.Fatalf("mention: %v", err)
	}

	if typ := nodeTypeOf(t, s, "entity:tank"); typ != "Widget" {
		t.Errorf("node type = %q, want the asserted type to stand", typ)
	}
}

// The imported code graph is not drift. `ingest-repo` writes files, functions
// and `imports` edges under a vocabulary the domain declares separately from
// the prose one, and nothing in the prose vocabulary ever will name them — so
// judging them against it reported every imported type as "invented by the
// extracting model", on a store where the model had extracted nothing. That
// buried the one line drift exists to surface: the type nobody declared
// anywhere.
func TestDriftSeparatesTheImportedCodeGraphFromInventedTypes(t *testing.T) {
	s := openStore(t,
		WithOntology("test", []string{"Node", "Gateway"}, []string{"backs"}),
		WithImportedVocabulary([]string{"file", "function"}, []string{"imports"}))
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "server.go", Type: "file"},
		cortexdb.ToolEntityInput{Name: "Serve", Type: "function"},
		cortexdb.ToolEntityInput{Name: "fast", Type: "StorageClass"})
	relate(t, s, "server.go", "Serve", "imports")

	d, err := s.DriftReport(context.Background())
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if want := map[string]int{"StorageClass": 1}; !reflect.DeepEqual(d.UndeclaredNodeTypes, want) {
		t.Errorf("undeclared nodes = %v, want %v — only the type no vocabulary names", d.UndeclaredNodeTypes, want)
	}
	if len(d.UndeclaredEdgeTypes) != 0 {
		t.Errorf("undeclared edges = %v, want none — imports is the code graph's", d.UndeclaredEdgeTypes)
	}
	if want := map[string]int{"file": 1, "function": 1}; !reflect.DeepEqual(d.ImportedNodeTypes, want) {
		t.Errorf("imported nodes = %v, want %v", d.ImportedNodeTypes, want)
	}
	if want := map[string]int{"imports": 1}; !reflect.DeepEqual(d.ImportedEdgeTypes, want) {
		t.Errorf("imported edges = %v, want %v", d.ImportedEdgeTypes, want)
	}
	if d.Clean() {
		t.Error("StorageClass is still drift")
	}
}

// A store holding nothing but the imported code graph is clean: every type in
// it is one somebody declared, just not in the prose list.
func TestAStoreOfOnlyImportedTypesIsClean(t *testing.T) {
	s := openStore(t,
		WithOntology("test", []string{"Node"}, []string{"backs"}),
		WithImportedVocabulary([]string{"file"}, []string{"imports"}))
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "a.go", Type: "file"},
		cortexdb.ToolEntityInput{Name: "b.go", Type: "file"})
	relate(t, s, "a.go", "b.go", "imports")

	d, err := s.DriftReport(context.Background())
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if !d.Clean() {
		t.Errorf("not clean: undeclared nodes %v, edges %v", d.UndeclaredNodeTypes, d.UndeclaredEdgeTypes)
	}
	if d.ImportedNodeTypes["file"] != 2 || d.ImportedEdgeTypes["imports"] != 1 {
		t.Errorf("imported = %v / %v, want file×2, imports×1", d.ImportedNodeTypes, d.ImportedEdgeTypes)
	}
}

// The prose vocabulary shouts and the code vocabulary whispers, and the two
// share words: CONTAINS is "a cluster contains a node", contains is "a file
// contains a function". The prose match was always case-insensitive and stays
// so; the imported match is exact, the way the importer's own admission check
// is, so a `Contains` that neither list spells is still reported as drift.
func TestImportedVocabularyMatchesExactly(t *testing.T) {
	undeclared, imported := partitionImported(
		map[string]int{"imports": 5, "Imports": 1, "layer_contains": 2},
		[]string{"imports", "layer_contains"})
	if want := map[string]int{"Imports": 1}; !reflect.DeepEqual(undeclared, want) {
		t.Errorf("undeclared = %v, want %v", undeclared, want)
	}
	if want := map[string]int{"imports": 5, "layer_contains": 2}; !reflect.DeepEqual(imported, want) {
		t.Errorf("imported = %v, want %v", imported, want)
	}
}
