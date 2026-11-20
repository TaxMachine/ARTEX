package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/TaxMachine/ARTEX/db"
	actool "github.com/TaxMachine/ARTEX/third_party/norma/tool"
)

// Knowledge-graph tools: the cross-task exploit-craft memory. Entities
// (primitives, techniques, crash signatures, binaries, functions) + typed
// edges, linked to findings/lessons/crossmap rows via refs. Agents search it
// when starting work (has this bug class been seen? which technique worked
// here before?) and record into it when they learn something durable. The
// human view is a generated Obsidian vault (API export).

// knowledgeRepo is the DB handle, set by the server (nil = tools unavailable).
var knowledgeRepo struct{ d *db.DB }

// SetKnowledgeRepo installs the knowledge-graph store for the agent tools.
func SetKnowledgeRepo(d *db.DB) { knowledgeRepo.d = d }

func (t *ToolSet) knowledgeAdd() actool.CoreTool {
	return writeTool("kn_add",
		"Add a node to the knowledge graph — the durable, cross-task exploit-craft memory. Record things the NEXT task benefits from: crash signatures (kind=crash_signature, dedupe on module+code+offset), memory-corruption primitives with their constraints (kind=primitive), techniques that worked (kind=technique — e.g. 'LFH spray via RTD COM objects in EXCEL'), environment facts (kind=environment — 'EXCEL COM needs DisplayAlerts=0 headless or it hangs'). refs link to existing rows: {\"finding_id\":14,\"binary\":\"EXCEL.EXE\"}. Edges connect to existing nodes by dedupe_key or id in the edges array: [{\"by\":\"excel-lfh-spray\",\"rel\":\"requires\"}].",
		obj(map[string]any{
			"kind":       map[string]any{"type": "string", "enum": []string{"primitive", "technique", "crash_signature", "binary", "function", "environment", "lesson"}, "description": "node type"},
			"name":       str("short title"),
			"body":       str("markdown: what it is, constraints, measured results"),
			"dedupe_key": str("stable identity, e.g. crash:EXCEL.EXE+c0000005+0x76a9d9 or spray:excel-lfh-com"),
			"refs":       map[string]any{"type": "object", "description": "links to existing rows: {\"finding_id\":14,\"binary\":\"EXCEL.EXE\",\"function\":\"Excel+0x76a9d9\"}"},
			"tags":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "search facets: [office, heap, lfh]"},
			"edges":      map[string]any{"type": "array", "items": map[string]any{"type": "object", "description": "{\"by\":\"<dedupe_key or numeric id>\",\"rel\":\"found_in|yields|mitigates|variant_of|requires|evolves_to|reproduces\"}"}, "description": "typed links from this node to existing ones"},
		}, "kind", "name", "dedupe_key"),
		t.needKnowledge("kn_add", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Kind      string           `json:"kind"`
				Name      string           `json:"name"`
				Body      string           `json:"body"`
				DedupeKey string           `json:"dedupe_key"`
				Refs      map[string]any   `json:"refs"`
				Tags      []string         `json:"tags"`
				Edges     []map[string]any `json:"edges"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.DedupeKey) == "" {
				return actool.Errorf("name and dedupe_key are required"), nil
			}
			var tid *int64
			if t.taskID > 0 {
				v := t.taskID
				tid = &v
			}
			id, err := knowledgeRepo.d.KnowledgeUpsert(db.KnowledgeNode{
				Kind: a.Kind, Name: a.Name, Body: a.Body, DedupeKey: a.DedupeKey,
				Refs: a.Refs, Tags: a.Tags, TaskID: tid, Source: "agent",
			})
			if err != nil {
				return actool.Errorf("kn_add failed: " + err.Error()), nil
			}
			linked := 0
			for _, e := range a.Edges {
				dst, ok := e["by"].(string)
				if !ok {
					continue
				}
				rel, _ := e["rel"].(string)
				if rel == "" {
					rel = "requires"
				}
				if knowledgeRepo.d.KnowledgeLink(id, dst, rel) == nil {
					linked++
				}
			}
			return jsonResult(map[string]any{"id": id, "dedupe_key": a.DedupeKey, "edges_created": linked})
		}))
}

func (t *ToolSet) knowledgeSearch() actool.CoreTool {
	return readTool("kn_search",
		"Search the knowledge graph (cross-task exploit-craft memory). Call it when starting work on a target or bug class — prior primitives, techniques that worked, crash signatures already seen (avoid re-triaging), environment gotchas. Full-text over name+body, or filter by kind/tags. neighbors=<id> expands a node's edges (what it requires/yields/evolves to).",
		obj(map[string]any{
			"q":         str("free-text query over name+body"),
			"kind":      str("optional: primitive|technique|crash_signature|binary|function|environment|lesson"),
			"tags":      str("optional: comma-separated tag filter"),
			"neighbors": map[string]any{"type": "integer", "description": "node id to expand edges for"},
			"limit":     map[string]any{"type": "integer", "description": "default 10"},
		}, ""),
		t.needKnowledge("kn_search", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Q         string `json:"q"`
				Kind      string `json:"kind"`
				Tags      string `json:"tags"`
				Neighbors int64  `json:"neighbors"`
				Limit     int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Limit <= 0 || a.Limit > 50 {
				a.Limit = 10
			}
			if a.Neighbors > 0 {
				nodes, edges, err := knowledgeRepo.d.KnowledgeNeighbors(a.Neighbors, a.Kind)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				return jsonResult(map[string]any{"center": a.Neighbors, "nodes": nodes, "edges": edges})
			}
			nodes, err := knowledgeRepo.d.KnowledgeSearch(a.Q, a.Kind, splitTags(a.Tags), a.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{"nodes": nodes})
		}))
}

func (t *ToolSet) needKnowledge(name string, run func(context.Context, json.RawMessage) (actool.Result, error)) func(context.Context, json.RawMessage) (actool.Result, error) {
	return func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		if knowledgeRepo.d == nil {
			return actool.Errorf(name + " is not available: the knowledge store is not initialized on this server"), nil
		}
		return run(ctx, in)
	}
}

func splitTags(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, strings.ToLower(t))
		}
	}
	return out
}

// unused-time guard
var _ = time.Second
var _ = fmt.Sprintf
