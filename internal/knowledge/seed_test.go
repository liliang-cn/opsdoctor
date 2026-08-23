package knowledge

import (
	"context"
	"testing"
)

// Retrieval returns CHUNKS; the graph holds ENTITIES extracted from them, under
// unrelated ids — a chunk is "corpus/guide.md#3", an entity is
// "entity:19216810410124". Mapping a chunk id through EntityNodeID therefore
// lands on nothing, the seed set holds nothing the graph contains, and expansion
// returns no neighbours at all. That is right for a CODE knowledge base, where a
// chunk IS the entity, and wrong for the prose an ops knowledge base is made of.
//
// Measured on a live 537-node graph before this: zero neighbours on every query,
// and zero whichever relation vocabulary was passed — GraphRAG had silently been
// plain vector search the whole time.
func TestSeedsComeFromEntityLabelsInTheRetrievedText(t *testing.T) {
	s := openTempStore(t)

	mustExec(t, s, `INSERT INTO graph_nodes (id, vector, content, node_type) VALUES
		('entity:drbd_reactor', X'', 'drbd-reactor', 'SystemdService'),
		('entity:nfs_kernel_server', X'', 'nfs-kernel-server', 'Package'),
		('entity:unrelated', X'', 'polymarket', 'Package')`)

	hits := []Hit{{Content: "The NFS gateway needs nfs-kernel-server installed before drbd-reactor can start it."}}
	got := s.seedsFromHitText(context.Background(), hits, map[string]struct{}{})

	if !contains(got, "entity:nfs_kernel_server") || !contains(got, "entity:drbd_reactor") {
		t.Errorf("seeds = %v, want both entities named in the text", got)
	}
	if contains(got, "entity:unrelated") {
		t.Errorf("seeds = %v, must not include an entity the text never mentions", got)
	}
}

// An address is not a concept the retrieved text is "about", and seeding on one
// spends the cap without bringing back anything a reader asked for.
func TestSeedsSkipLabelsThatAreJustAddresses(t *testing.T) {
	s := openTempStore(t)
	mustExec(t, s, `INSERT INTO graph_nodes (id, vector, content, node_type) VALUES
		('entity:vip', X'', '192.168.123.200/24', 'VIP'),
		('entity:gw', X'', 'iscsi-gw', 'Gateway')`)

	hits := []Hit{{Content: "iscsi-gw exports on 192.168.123.200/24"}}
	got := s.seedsFromHitText(context.Background(), hits, map[string]struct{}{})

	if contains(got, "entity:vip") {
		t.Errorf("seeds = %v, must not seed on a bare CIDR", got)
	}
	if !contains(got, "entity:gw") {
		t.Errorf("seeds = %v, want the named gateway", got)
	}
}

// Already-seeded ids must not be repeated: the cap is small and a duplicate
// spends it twice on the same neighbourhood.
func TestSeedsSkipWhatIsAlreadySeeded(t *testing.T) {
	s := openTempStore(t)
	mustExec(t, s, `INSERT INTO graph_nodes (id, vector, content, node_type) VALUES
		('entity:drbd_reactor', X'', 'drbd-reactor', 'SystemdService')`)

	seen := map[string]struct{}{"entity:drbd_reactor": {}}
	got := s.seedsFromHitText(context.Background(), []Hit{{Content: "drbd-reactor"}}, seen)
	if len(got) != 0 {
		t.Errorf("seeds = %v, want none — it was already seeded", got)
	}
}

func TestLetterCount(t *testing.T) {
	for in, want := range map[string]int{
		"192.168.1.1/24": 0,
		"drbd-reactor":   11,
		"":               0,
		"中文":             2,
	} {
		if got := letterCount(in); got != want {
			t.Errorf("letterCount(%q) = %d, want %d", in, got, want)
		}
	}
}

// openTempStore is openStore with no declared vocabulary: seeding by label is
// pure SQL over the graph tables and never consults the ontology.
//
// cortexdb creates the graph tables at open, so the rows these tests insert go
// into the real schema — which is why they must carry a (here empty) vector blob.
func openTempStore(t *testing.T) *Store {
	t.Helper()
	return openStore(t)
}

func mustExec(t *testing.T, s *Store, q string) {
	t.Helper()
	if _, err := s.db.SQL().ExecContext(context.Background(), q); err != nil {
		t.Fatalf("seed graph: %v", err)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
