package knowledge

import (
	"context"
	"sort"
)

// Knowledge that is the same everywhere — a product's documentation, its code
// graph, the reference of the CLI it ships — belongs in one base built once and
// installed as a file, not re-ingested cluster by cluster. Built separately, two
// deployments of the same product ended up with bases that shared almost
// nothing: one had the DRBD manuals and none of the product's own docs, the
// other the reverse. What is local to one deployment — its incident notes,
// what was tried on which machine — stays in the base it writes to.
//
// A Store with shared bases attached answers SearchGraph and Walk from all of
// them. Writes never reach a shared base: ingesting, purging and resolving act
// on the Store alone.

// AttachShared adds read-only bases to search alongside s. The Store takes
// ownership and closes them on Close.
func (s *Store) AttachShared(shared ...*Store) {
	for _, sh := range shared {
		if sh != nil && sh != s {
			s.shared = append(s.shared, sh)
		}
	}
}

// SearchGraph runs graph retrieval over this base and every shared one, and
// merges the results: hits interleaved by score, the same chunk once, and the
// neighbours of all of them under the same cap a single base applies.
func (s *Store) SearchGraph(ctx context.Context, query string, topK int) (*GraphResult, error) {
	own, err := s.searchGraphLocal(ctx, query, topK)
	if len(s.shared) == 0 || err != nil {
		return own, err
	}
	if topK <= 0 {
		topK = 6
	}
	results := []*GraphResult{own}
	for _, sh := range s.shared {
		// A shared base that fails to answer costs its share of the results,
		// not the whole search: the local base is still worth returning.
		if r, err := sh.searchGraphLocal(ctx, query, topK); err == nil && r != nil {
			results = append(results, r)
		}
	}
	return mergeGraphResults(results, topK), nil
}

func mergeGraphResults(results []*GraphResult, topK int) *GraphResult {
	out := &GraphResult{}
	seenHit := map[string]bool{}
	var hits []Hit
	for _, r := range results {
		if r == nil {
			continue
		}
		for _, h := range r.Hits {
			key := h.DocumentID + "\x00" + h.Content
			if seenHit[key] {
				continue
			}
			seenHit[key] = true
			hits = append(hits, h)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > topK {
		hits = hits[:topK]
	}
	out.Hits = hits

	seenN := map[string]bool{}
	for _, r := range results {
		if r == nil {
			continue
		}
		for _, n := range r.Neighbors {
			if seenN[n.ID] || len(out.Neighbors) >= graphNeighborsTotal {
				continue
			}
			seenN[n.ID] = true
			out.Neighbors = append(out.Neighbors, n)
		}
	}
	return out
}

// matchRank orders how confidently a walk resolved its starting name.
var matchRank = map[string]int{"exact": 3, "fold": 2, "contains": 1}

// Walk resolves the starting entity in every base and walks from the best
// match. A graph lives in one base, so the walk does not cross between them:
// an entity from the shared code graph is walked there, one from a local
// incident note locally. On a tie the local base wins, because what this
// deployment recorded about itself is the more specific answer.
func (s *Store) Walk(ctx context.Context, name, relation, direction string) (*WalkResult, error) {
	best, firstErr := s.walkLocal(ctx, name, relation, direction)
	if len(s.shared) == 0 {
		return best, firstErr
	}
	rank := func(r *WalkResult) int {
		if r == nil || r.From == "" {
			return 0
		}
		return matchRank[r.Match] + 1 // a resolved start beats none even if Match is empty
	}
	for _, sh := range s.shared {
		r, err := sh.walkLocal(ctx, name, relation, direction)
		if err != nil {
			continue
		}
		if rank(r) > rank(best) {
			best, firstErr = r, nil
		}
	}
	if rank(best) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return best, nil
}
