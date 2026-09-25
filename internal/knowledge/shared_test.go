package knowledge

import (
	"context"
	"testing"

	cortexdb "github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// The product graph lives in the shared base, the deployment's own notes in
// the local one; a walk must find an entity wherever it lives.
func TestWalkFindsAnEntityThatLivesInTheSharedBase(t *testing.T) {
	local := openStore(t, WithRelationTypes([]string{"backs"}))
	upsert(t, local, cortexdb.ToolEntityInput{Name: "node-a", Type: "Node"})
	local.AttachShared(storageStore(t))

	got, err := local.Walk(context.Background(), "r0", "backs", WalkIn)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got.Steps) != 1 || got.Steps[0].Name != "tank" {
		t.Fatalf("steps = %+v, want the pool from the shared base", got.Steps)
	}
}

// When both bases know the name, the local one answers: what this deployment
// recorded about itself is the more specific account.
func TestWalkPrefersTheLocalBaseOnATie(t *testing.T) {
	local := storageStore(t)
	shared := openStore(t, WithRelationTypes([]string{"backs"}))
	upsert(t, shared,
		cortexdb.ToolEntityInput{Name: "other-pool", Type: "StoragePool"},
		cortexdb.ToolEntityInput{Name: "r0", Type: "DRBDResource"})
	relate(t, shared, "other-pool", "r0", "backs")
	local.AttachShared(shared)

	got, err := local.Walk(context.Background(), "r0", "backs", WalkIn)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got.Steps) != 1 || got.Steps[0].Name != "tank" {
		t.Fatalf("steps = %+v, want the local pool", got.Steps)
	}
}

func TestMergeGraphResultsInterleavesByScoreAndDedupes(t *testing.T) {
	a := &GraphResult{Hits: []Hit{{DocumentID: "local#1", Content: "x", Score: 0.5}},
		Neighbors: []Neighbor{{ID: "n1"}}}
	b := &GraphResult{Hits: []Hit{
		{DocumentID: "shared#1", Content: "y", Score: 0.9},
		{DocumentID: "local#1", Content: "x", Score: 0.5},
		{DocumentID: "shared#2", Content: "z", Score: 0.1},
	}, Neighbors: []Neighbor{{ID: "n1"}, {ID: "n2"}}}
	got := mergeGraphResults([]*GraphResult{a, b}, 2)
	if len(got.Hits) != 2 || got.Hits[0].DocumentID != "shared#1" || got.Hits[1].DocumentID != "local#1" {
		t.Fatalf("hits = %+v, want the two best, highest first, no duplicate", got.Hits)
	}
	if len(got.Neighbors) != 2 {
		t.Errorf("neighbours = %+v, want n1 once and n2", got.Neighbors)
	}
}
