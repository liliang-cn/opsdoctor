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
