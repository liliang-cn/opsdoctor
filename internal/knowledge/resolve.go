package knowledge

import (
	"context"
	"fmt"

	"github.com/liliang-cn/cortexdb/v2/pkg/graphflow"
)

// Closing a split concept, which reporting one cannot do.
//
// Entity ids keep separators, so "DRBDResource" and "DRBD resource" are two
// nodes for one thing and each document's edges attach to whichever spelling it
// used. A walk pools the spellings so answers stop depending on how the caller
// typed the name, but the graph still holds two entities: expansion counts them
// separately, the neighbour quota gives each its own slot, and the split
// survives every re-query. Merging is what actually ends it.
//
// The merge is cortexdb's — it groups by the same case-and-separator key a
// pooled walk uses, repoints edges onto the survivor, and records the losing
// spelling as an alias so a caller who knew that name still resolves. What it
// cannot know is which nodes are ours: it reads a node's content as the name,
// and the code graph in the same store leaves every symbol's bare name there,
// so seven packages' main.go share one key. Passing the domain's declared types
// is what keeps the merge to the vocabulary that has spellings at all.

// SpellingGroup is one concept and the spellings folded into it.
type SpellingGroup struct {
	Canonical string   `json:"canonical"`
	Aliases   []string `json:"aliases"`
}

// SpellingReport is what a resolution did, or would do.
type SpellingReport struct {
	// Merged counts the nodes removed, not the concepts affected: three
	// spellings of one name is one group and two merges.
	Merged int             `json:"merged"`
	Groups []SpellingGroup `json:"groups"`
	DryRun bool            `json:"dry_run,omitempty"`
}

// ResolveSpellings merges entities that are one concept spelled several ways.
//
// dryRun reports the merges without making them, which is the right default for
// an operator: deleting a node is not reversible, and the list is short enough
// to read.
func (s *Store) ResolveSpellings(ctx context.Context, dryRun bool) (*SpellingReport, error) {
	if len(s.entityTypes) == 0 {
		// With no declared vocabulary every entity node is the code graph's,
		// where a shared name means two files rather than two spellings.
		return &SpellingReport{DryRun: dryRun}, nil
	}
	rep, err := graphflow.ResolveEntities(ctx, s.db, graphflow.ResolveOptions{
		DryRun:    dryRun,
		NodeTypes: s.entityTypes,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve spellings: %w", err)
	}
	out := &SpellingReport{Merged: rep.EntitiesMerged, DryRun: dryRun}
	for _, g := range rep.Groups {
		out.Groups = append(out.Groups, SpellingGroup{Canonical: g.Canonical, Aliases: g.Aliases})
	}
	return out, nil
}
