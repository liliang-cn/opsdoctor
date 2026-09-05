package domain

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// error_patterns must keep working untouched: it is folded in as one group so a
// domain that declares only the old field behaves exactly as before.
func TestSourceGroupsFoldsInErrorPatterns(t *testing.T) {
	d := &Domain{ErrorPatternsRaw: []string{`pr_err\("([^"]+)"`}}
	if err := compileForTest(d, ""); err != nil {
		t.Fatal(err)
	}
	groups := d.SourceGroups()
	if len(groups) != 1 || groups[0].Name != "errors" {
		t.Fatalf("groups = %+v, want one named errors", groups)
	}
	// A message shorter than a log line is a fragment; the old behaviour.
	if groups[0].MinLength != 8 {
		t.Errorf("errors MinLength = %d, want 8", groups[0].MinLength)
	}
}

// source_patterns is retired: mining types/operations/constants by regex was a
// degraded tree-sitter, and code structure now arrives as a real graph via
// ingest-repo (Understand-Anything → graphimport). A domain.toml still carrying
// the table loads fine — TOML decoding ignores unknown keys — and only its
// error_patterns are mined.
func TestSourcePatternsTableIsIgnored(t *testing.T) {
	d := &Domain{ErrorPatternsRaw: []string{`pr_err\("([^"]+)"`}}
	if err := compileForTest(d, "\n[[source_patterns]]\nname = \"types\"\npatterns = ['type\\s+([A-Z]\\w+)\\s+struct']\n"); err != nil {
		t.Fatalf("a domain.toml with a leftover source_patterns table must still load: %v", err)
	}
	groups := d.SourceGroups()
	if len(groups) != 1 || groups[0].Name != "errors" {
		t.Fatalf("groups = %+v, want the errors group alone", groups)
	}
}

// compileForTest runs the same compilation Load does, without a file. extra is
// appended to the TOML body verbatim.
func compileForTest(d *Domain, extra string) error {
	d.Name, d.Persona = "t", "p"
	tmp := filepath.Join(os.TempDir(), "opsdoctor-domain-test.toml")
	body := "name = \"t\"\npersona = \"p\"\n"
	for _, p := range d.ErrorPatternsRaw {
		body += "error_patterns = ['" + p + "']\n"
	}
	body += extra
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	loaded, err := Load(tmp)
	if err != nil {
		return err
	}
	*d = *loaded
	return nil
}

// The code vocabulary is separate from the prose vocabulary because they have
// different consumers. The prose types go into the LLM extractor's prompt
// ("Use ONLY these entity types"); the code types must not, or the extractor is
// told it may invent `function` and `calls` nodes out of documentation.
func TestCodeVocabularyIsSeparateFromProse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "domain.toml")
	if err := os.WriteFile(path, []byte(`
name = "x"
persona = "y"
entity_types = ["Cluster", "Node"]
relation_types = ["MANAGES"]

[vocabulary.code]
entity_types = ["file", "function"]
relation_types = ["imports", "calls"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.EntityTypes) != 2 || d.EntityTypes[0] != "Cluster" {
		t.Errorf("prose entity types = %v, want the domain concepts only", d.EntityTypes)
	}
	if len(d.Vocabulary.Code.RelationTypes) != 2 {
		t.Errorf("code relation types = %v", d.Vocabulary.Code.RelationTypes)
	}
	// The prose vocabulary must not have absorbed the code vocabulary.
	for _, tp := range d.RelationTypes {
		if tp == "imports" || tp == "calls" {
			t.Fatalf("code type %q leaked into the prose vocabulary %v", tp, d.RelationTypes)
		}
	}
}

// Resolution records how the code edges were derived. A syntactic extractor
// cannot resolve Go's implicit interfaces or distinguish two same-named
// methods, so an importer must be able to say which it is rather than let a
// consumer assume every edge is type-checked.
func TestCodeVocabularyResolutionDefaultsToSyntactic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "domain.toml")
	if err := os.WriteFile(path, []byte(`
name = "x"
persona = "y"

[vocabulary.code]
relation_types = ["calls"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Vocabulary.Code.Resolution; got != "syntactic" {
		t.Errorf("resolution = %q, want syntactic by default", got)
	}
}

func TestCodeVocabularyRejectsUnknownResolution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "domain.toml")
	if err := os.WriteFile(path, []byte(`
name = "x"
persona = "y"

[vocabulary.code]
resolution = "vibes"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "resolution") {
		t.Fatalf("expected an unknown resolution to be rejected, got %v", err)
	}
}

// AllowsRelation is what the importer gates on. It is case-sensitive on
// purpose: the prose vocabulary shouts (MANAGES, DEPENDS_ON) and the code
// vocabulary whispers (contains, depends_on), and folding their cases together
// would silently merge "a cluster contains a node" with "a file contains a
// function" — two different relations that happen to share a word.
func TestAllowsRelationIsCaseSensitive(t *testing.T) {
	d := &Domain{}
	d.Vocabulary.Code.RelationTypes = []string{"contains", "calls"}

	if !d.AllowsCodeRelation("contains") {
		t.Error("declared type must be allowed")
	}
	if d.AllowsCodeRelation("CONTAINS") {
		t.Error("CONTAINS is a domain relation, not the code one; it must not match")
	}
	if d.AllowsCodeRelation("implements") {
		t.Error("undeclared type must be refused")
	}
}

// A domain that says nothing about code still admits an Understand-Anything
// graph. Requiring the list would be a transcription task with a cliff at the
// end: the importer refuses a whole graph over one undeclared type.
func TestCodeVocabularyDefaultsToUpstreamUnions(t *testing.T) {
	d := &Domain{}
	if err := compileForTest(d, ""); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"file", "function", "class"} {
		if !d.AllowsCodeEntity(want) {
			t.Errorf("default code vocabulary is missing node type %q", want)
		}
	}
	for _, want := range []string{"calls", "imports", "implements", "tested_by"} {
		if !d.AllowsCodeRelation(want) {
			t.Errorf("default code vocabulary is missing edge type %q", want)
		}
	}
	if d.Vocabulary.Code.Resolution != ResolutionSyntactic {
		t.Errorf("resolution = %q, want the honest default %q", d.Vocabulary.Code.Resolution, ResolutionSyntactic)
	}
}

// An explicit list replaces the default rather than extending it: narrowing the
// admission list is how a domain keeps its graph small, and a merge would make
// that impossible.
func TestCodeVocabularyExplicitListWins(t *testing.T) {
	d := &Domain{}
	extra := "\n[vocabulary.code]\nentity_types = ['file']\nrelation_types = ['contains']\n"
	if err := compileForTest(d, extra); err != nil {
		t.Fatal(err)
	}
	if !d.AllowsCodeEntity("file") || d.AllowsCodeEntity("function") {
		t.Errorf("entity types = %v, want exactly [file]", d.Vocabulary.Code.EntityTypes)
	}
	if !d.AllowsCodeRelation("contains") || d.AllowsCodeRelation("calls") {
		t.Errorf("relation types = %v, want exactly [contains]", d.Vocabulary.Code.RelationTypes)
	}
}

// Ends are validated against entity_types rather than trusted, because cortexdb
// refuses a whole schema whose link side names an object type it does not
// declare — and registration is best-effort, so the refusal is one log line and
// then a knowledge base silently running with no ontology. A typo should cost
// the load, loudly.
func TestRelationEndsMustNameDeclaredEntityTypes(t *testing.T) {
	err := loadTOML(t, `
entity_types = ["StoragePool", "DRBDResource"]

[[relation]]
name = "backs"
from = "StoragePool"
to   = "DRBDResorce"
`)
	if err == nil {
		t.Fatal("a to = naming an undeclared entity type must fail the load")
	}
	if !strings.Contains(err.Error(), "DRBDResorce") {
		t.Errorf("error = %v, want it to name the typo", err)
	}
}

// Listing a relation in relation_types AND giving it ends below is the natural
// way to write the file: the flat list is the vocabulary, the blocks refine part
// of it. Merging must not duplicate the name.
func TestRelationEndsMergeIntoTheEdgeVocabulary(t *testing.T) {
	d := mustLoadTOML(t, `
entity_types = ["StoragePool", "DRBDResource"]
relation_types = ["backs", "contains"]

[[relation]]
name = "backs"
from = "StoragePool"
to   = "DRBDResource"
`)
	if want := []string{"backs", "contains"}; !reflect.DeepEqual(d.RelationTypes, want) {
		t.Errorf("relation types = %v, want %v — declaring ends must not duplicate the name", d.RelationTypes, want)
	}
}

// A relation given ends without being listed above is still part of the edge
// vocabulary; expansion filters on that list, so a name missing from it is an
// edge type nothing walks.
func TestARelationDeclaredOnlyWithEndsIsStillInTheVocabulary(t *testing.T) {
	d := mustLoadTOML(t, `
entity_types = ["StoragePool", "DRBDResource"]

[[relation]]
name = "backs"
from = "StoragePool"
to   = "DRBDResource"
`)
	if want := []string{"backs"}; !reflect.DeepEqual(d.RelationTypes, want) {
		t.Errorf("relation types = %v, want %v", d.RelationTypes, want)
	}
}

// Two blocks for one name are two answers to "what sits on each end", and only
// one can be registered — which one would be decided by file order.
func TestARelationCannotDeclareTwoSetsOfEnds(t *testing.T) {
	err := loadTOML(t, `
entity_types = ["A", "B", "C"]

[[relation]]
name = "backs"
from = "A"
to   = "B"

[[relation]]
name = "backs"
from = "A"
to   = "C"
`)
	if err == nil {
		t.Fatal("declaring one relation's ends twice must fail the load")
	}
}

// A domain that declares edge names only must load exactly as before: ends are
// optional, and most domain.toml files in the wild predate them.
func TestEdgeNamesWithoutEndsStillLoad(t *testing.T) {
	d := mustLoadTOML(t, `
entity_types = ["StoragePool"]
relation_types = ["backs", "contains"]
`)
	if len(d.Relations) != 0 {
		t.Errorf("relations = %v, want none declared", d.Relations)
	}
	if want := []string{"backs", "contains"}; !reflect.DeepEqual(d.RelationTypes, want) {
		t.Errorf("relation types = %v, want %v", d.RelationTypes, want)
	}
}

func loadTOML(t *testing.T, body string) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "domain.toml")
	if err := os.WriteFile(path, []byte("name = \"t\"\npersona = \"p\"\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	return err
}

func mustLoadTOML(t *testing.T, body string) *Domain {
	t.Helper()
	path := filepath.Join(t.TempDir(), "domain.toml")
	if err := os.WriteFile(path, []byte("name = \"t\"\npersona = \"p\"\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return d
}

// An end may be one type or several. The single-string form is not sugar for a
// one-element list — it is the form almost every relation wants, and requiring
// from = ["StoragePool"] would make the common case pay for the rare one.
func TestARelationEndTakesAStringOrAList(t *testing.T) {
	d := mustLoadTOML(t, `
entity_types = ["Snapshot", "Backup", "Volume", "StoragePool", "DRBDResource"]

[[relation]]
name = "protects"
from = ["Snapshot", "Backup"]
to   = "Volume"

[[relation]]
name = "backs"
from = "StoragePool"
to   = "DRBDResource"
`)
	if len(d.Relations) != 2 {
		t.Fatalf("relations = %+v, want two", d.Relations)
	}
	if want := []string{"Snapshot", "Backup"}; !reflect.DeepEqual([]string(d.Relations[0].From), want) {
		t.Errorf("protects from = %v, want %v", d.Relations[0].From, want)
	}
	if want := []string{"Volume"}; !reflect.DeepEqual([]string(d.Relations[0].To), want) {
		t.Errorf("protects to = %v, want %v", d.Relations[0].To, want)
	}
	if want := []string{"StoragePool"}; !reflect.DeepEqual([]string(d.Relations[1].From), want) {
		t.Errorf("backs from = %v, want %v", d.Relations[1].From, want)
	}
}

// Every type in a list is checked, not just the first: cortexdb refuses a whole
// schema whose link side names an unknown type, and registration is
// best-effort, so a typo in the second entry would cost the ontology silently.
func TestEveryTypeInAPolymorphicEndIsChecked(t *testing.T) {
	err := loadTOML(t, `
entity_types = ["Snapshot", "Volume"]

[[relation]]
name = "protects"
from = ["Snapshot", "Backupp"]
to   = "Volume"
`)
	if err == nil || !strings.Contains(err.Error(), "Backupp") {
		t.Fatalf("expected the typo in the second type to fail the load, got %v", err)
	}
}

// An edge's direction is recovered by matching its endpoints against the two
// ends. Disjoint ends work, and identical ends work — a self-relation reads the
// same either way. In between an edge matches both readings, and cortexdb
// refuses such a schema; catching it here makes it cost the load rather than
// the ontology.
func TestEndsThatPartiallyOverlapAreRejected(t *testing.T) {
	err := loadTOML(t, `
entity_types = ["Snapshot", "Backup", "Volume"]

[[relation]]
name = "protects"
from = ["Snapshot", "Backup"]
to   = ["Backup", "Volume"]
`)
	if err == nil || !strings.Contains(err.Error(), "Backup") {
		t.Fatalf("expected partially overlapping ends to fail the load, got %v", err)
	}
}

// A self-relation is the legitimate case of ends that overlap completely.
func TestIdenticalEndsAreAcceptedAsASelfRelation(t *testing.T) {
	d := mustLoadTOML(t, `
entity_types = ["DRBDResource"]

[[relation]]
name = "conflicts_with"
from = "DRBDResource"
to   = "DRBDResource"
`)
	if len(d.Relations) != 1 {
		t.Fatalf("relations = %+v, want the self-relation to load", d.Relations)
	}
}

// A list of something other than strings is a mistake worth naming, not a
// panic and not a silently empty end.
func TestARelationEndRejectsANonStringList(t *testing.T) {
	err := loadTOML(t, `
entity_types = ["Volume"]

[[relation]]
name = "protects"
from = [1, 2]
to   = "Volume"
`)
	if err == nil || !strings.Contains(err.Error(), "string") {
		t.Fatalf("expected a non-string end to fail the load, got %v", err)
	}
}
