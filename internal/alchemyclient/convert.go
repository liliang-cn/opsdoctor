package alchemyclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	cortexdb "github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"

	"github.com/liliang-cn/opsdoctor/internal/knowledge"
)

// Metadata keys written on every node and edge alchemy produced. They are
// what makes an answer accountable: which job, which producer, whether a
// model proposed the fact or something stated it, under which vocabulary.
const (
	MetaJob        = "alchemy_job"
	MetaEntityID   = "alchemy_entity_id"
	MetaProducer   = "alchemy_producer"
	MetaStated     = "alchemy_stated"
	MetaOntology   = "alchemy_ontology"
	MetaModel      = "alchemy_model"
	MetaConfidence = "alchemy_confidence"
	MetaAliases    = "alchemy_aliases"
	MetaSource     = "alchemy_source"
)

// graphOf turns alchemy's result into the store's write.
//
// alchemy names an entity by a job-local id and cites the chunk it read by
// index; the store names an entity by what it is called — which is how the
// same gateway mentioned in forty documents is one node — and cites a chunk
// by the id it embedded it under. Because every source uploaded was one of
// the store's chunks read whole, alchemy's chunk source IS the store's chunk
// id, and the join is a lookup rather than a guess.
func graphOf(res alchemy.Result) *knowledge.DocumentGraph {
	dg := &knowledge.DocumentGraph{}
	chunkIDs := make(map[int]string, len(res.Chunks))
	for _, c := range res.Chunks {
		chunkIDs[c.Index] = c.Source
	}
	nameOf := make(map[string]string, len(res.Entities))
	for _, e := range res.Entities {
		name := strings.TrimSpace(e.Name)
		if name == "" {
			dg.Notes = append(dg.Notes, fmt.Sprintf("alchemy entity %s has no name and was dropped", e.ID))
			continue
		}
		nameOf[e.ID] = name
		in := cortexdb.ToolEntityInput{
			Name: name, Type: e.Type, Metadata: provenanceMeta(res.Job, e.Provenance),
		}
		in.Metadata[MetaEntityID] = e.ID
		if d, ok := e.Attributes["description"].(string); ok {
			in.Description = d
		} else if d, ok := e.Attributes["summary"].(string); ok {
			in.Description = d
		}
		if len(e.Aliases) > 0 {
			if b, err := json.Marshal(e.Aliases); err == nil {
				in.Metadata[MetaAliases] = string(b)
			}
		}
		if id, ok := chunkIDs[e.Provenance.Chunk]; ok && id != "" {
			in.ChunkIDs = []string{id}
		}
		dg.Entities = append(dg.Entities, in)
	}
	for _, r := range res.Relations {
		from, okF := nameOf[r.From]
		to, okT := nameOf[r.To]
		if !okF || !okT {
			dg.Notes = append(dg.Notes, fmt.Sprintf("alchemy relation %s -[%s]-> %s names an entity the result does not carry and was dropped", r.From, r.Type, r.To))
			continue
		}
		in := cortexdb.ToolRelationInput{
			From: from, To: to, Type: r.Type,
			Inferred:   !r.Provenance.Producer.Deterministic(),
			Provenance: renderProvenance(res.Job, r.Provenance),
			Metadata:   provenanceMeta(res.Job, r.Provenance),
		}
		if id, ok := chunkIDs[r.Provenance.Chunk]; ok && id != "" {
			in.ChunkIDs = []string{id}
		}
		dg.Relations = append(dg.Relations, in)
	}
	for _, v := range res.Violations {
		dg.Notes = append(dg.Notes, fmt.Sprintf("alchemy: %s %q: %s", v.Kind, v.Subject, v.Detail))
	}
	for _, d := range res.Duplicates {
		dg.Notes = append(dg.Notes, fmt.Sprintf("alchemy: possible duplicate %s: %s", d.Subject, d.Detail))
	}
	for _, u := range res.Unread {
		dg.Notes = append(dg.Notes, fmt.Sprintf("alchemy: could not read %s: %s", u.Source, u.Reason))
	}
	return dg
}

func provenanceMeta(job string, p alchemy.Provenance) map[string]string {
	m := map[string]string{
		MetaJob:      job,
		MetaProducer: string(p.Producer),
		MetaStated:   strconv.FormatBool(p.Producer.Deterministic()),
		MetaSource:   p.Source,
	}
	if p.Ontology != "" {
		m[MetaOntology] = p.Ontology
	}
	if p.Model != "" {
		m[MetaModel] = p.Model
	}
	if p.Confidence > 0 {
		m[MetaConfidence] = strconv.FormatFloat(p.Confidence, 'f', 2, 64)
	}
	return m
}

func renderProvenance(job string, p alchemy.Provenance) string {
	return fmt.Sprintf("alchemy:%s:%s:%s", job, p.Producer, p.Source)
}

// idempotencyKey names the document as alchemy will see it. A re-ingest of an
// unchanged document under an unchanged vocabulary is a replay on the server
// rather than a second bill; any change to either is new work.
func idempotencyKey(ontologyID, docID string, chunks []string) string {
	h := sha256.New()
	h.Write([]byte(ontologyID))
	h.Write([]byte{0})
	h.Write([]byte(docID))
	for _, c := range chunks {
		h.Write([]byte{0x1e})
		h.Write([]byte(c))
	}
	return "opsdoctor:" + hex.EncodeToString(h.Sum(nil))[:32]
}
