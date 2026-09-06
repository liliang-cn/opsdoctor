package alchemyclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/ontology"

	"github.com/liliang-cn/opsdoctor/internal/domain"
)

// OntologyFor translates the domain's prose vocabulary into the ontology
// alchemy checks a job against, and returns it with its id.
//
// The id is "<domain slug>@<fingerprint of the vocabulary>". alchemy writes
// the id onto every entity and edge it produces, so a graph's provenance
// says which edit of domain.toml checked it — the same fact the store's own
// drift report keeps, arrived at from the other side.
//
// Only the prose part is declared. The code vocabulary admits graphs that
// `ingest-repo` imports deterministically; a prose extractor told it may
// emit `function` and `calls` invents code structure out of documentation.
func OntologyFor(dom *domain.Domain) (blob []byte, id string, err error) {
	if dom == nil || len(dom.EntityTypes) == 0 {
		return nil, "", fmt.Errorf("alchemy: the domain declares no entity_types; alchemy has no unconstrained mode, so it needs the vocabulary in domain.toml")
	}
	ends := make(map[string]domain.Relation, len(dom.Relations))
	for _, r := range dom.Relations {
		ends[strings.ToLower(strings.TrimSpace(r.Name))] = r
	}
	var v ontology.Vocabulary
	for _, t := range dom.EntityTypes {
		if t = strings.TrimSpace(t); t != "" {
			v.Entities = append(v.Entities, ontology.EntityType{Name: t})
		}
	}
	for _, t := range dom.RelationTypes {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		rt := ontology.RelationType{Name: t}
		if e, ok := ends[strings.ToLower(t)]; ok {
			rt.From, rt.To = []string(e.From), []string(e.To)
			// The three flags alchemy holds a job on. They are the whole of
			// what it can refuse for: without BothWays a symmetric relation
			// stops every ingest that extracts the pair, and without the two
			// cardinality flags a corpus that corrects itself reports no
			// disagreement at all.
			rt.BothWays, rt.AtMostOneIn, rt.AtMostOneOut = e.BothWays, e.AtMostOneIn, e.AtMostOneOut
		}
		v.Relations = append(v.Relations, rt)
	}
	id = slug(dom.Name) + "@" + fingerprint(v)
	blob, err = json.MarshalIndent(struct {
		ID    string                                `json:"id"`
		Parts map[ontology.Part]ontology.Vocabulary `json:"parts"`
	}{ID: id, Parts: map[ontology.Part]ontology.Vocabulary{ontology.PartProse: v}}, "", "  ")
	if err != nil {
		return nil, "", fmt.Errorf("alchemy: render ontology: %w", err)
	}
	return blob, id, nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(name string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if s == "" {
		return "domain"
	}
	return s
}

// fingerprint digests the vocabulary as a set: reordering domain.toml is not
// a change of meaning and must not change the id.
func fingerprint(v ontology.Vocabulary) string {
	ents := make([]string, 0, len(v.Entities))
	for _, e := range v.Entities {
		ents = append(ents, e.Name)
	}
	rels := make([]string, 0, len(v.Relations))
	for _, r := range v.Relations {
		from := append([]string(nil), r.From...)
		to := append([]string(nil), r.To...)
		sort.Strings(from)
		sort.Strings(to)
		// The flags are part of the fingerprint because they are part of the
		// meaning: turning on AtMostOneIn changes which graphs the ontology
		// admits, and a graph extracted before the change was checked against
		// a different vocabulary. An id that did not move would say otherwise.
		rels = append(rels, fmt.Sprintf("%s\x1f%s\x1f%s\x1f%t\x1f%t\x1f%t",
			r.Name, strings.Join(from, ","), strings.Join(to, ","),
			r.BothWays, r.AtMostOneIn, r.AtMostOneOut))
	}
	sort.Strings(ents)
	sort.Strings(rels)
	sum := sha256.Sum256([]byte(strings.Join(ents, "\x00") + "\x1e" + strings.Join(rels, "\x00")))
	return hex.EncodeToString(sum[:6])
}
