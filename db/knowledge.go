package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Knowledge graph store: cross-task exploit-craft memory (entities + typed
// edges), linked to findings/lessons via the refs jsonb.

type KnowledgeNode struct {
	ID        int64          `json:"id"`
	Kind      string         `json:"kind"`
	Name      string         `json:"name"`
	Body      string         `json:"body"`
	Refs      map[string]any `json:"refs"`
	Tags      []string       `json:"tags"`
	DedupeKey string         `json:"dedupe_key"`
	Source    string         `json:"source"`
	TaskID    *int64         `json:"task_id,omitempty"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type KnowledgeEdge struct {
	ID   int64  `json:"id"`
	Src  int64  `json:"src"`
	Dst  int64  `json:"dst"`
	Rel  string `json:"rel"`
	Note string `json:"note"`
}

// KnowledgeUpsert inserts or updates a node by dedupe_key (body/refs/tags
// merge: non-empty incoming wins) and returns the id.
func (d *DB) KnowledgeUpsert(n KnowledgeNode) (int64, error) {
	tags, _ := json.Marshal(n.Tags)
	if len(tags) == 0 || string(tags) == "null" {
		tags = []byte(`[]`)
	}
	refs, _ := json.Marshal(n.Refs)
	if len(refs) == 0 || string(refs) == "null" {
		refs = []byte(`{}`)
	}
	var taskID any
	if n.TaskID != nil && *n.TaskID > 0 {
		taskID = *n.TaskID
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO knowledge_nodes (kind, name, body, refs, tags, dedupe_key, source, task_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (dedupe_key) DO UPDATE SET
  name = EXCLUDED.name,
  body = CASE WHEN EXCLUDED.body = '' THEN knowledge_nodes.body ELSE EXCLUDED.body END,
  refs = knowledge_nodes.refs || EXCLUDED.refs,
  tags = (SELECT ARRAY(SELECT DISTINCT unnest(knowledge_nodes.tags || EXCLUDED.tags))),
  updated_at = now()
RETURNING id`,
		n.Kind, n.Name, n.Body, string(refs), string(tags), n.DedupeKey, n.Source, taskID).Scan(&id)
	return id, err
}

// KnowledgeLink creates a typed edge src->dst where dst is a dedupe_key or a
// numeric id in string form.
func (d *DB) KnowledgeLink(src int64, dstBy, rel string) error {
	var dst int64
	if n, err := fmt.Sscanf(dstBy, "%d", &dst); n != 1 || err != nil {
		// resolve by dedupe_key
		err := d.QueryRow(`SELECT id FROM knowledge_nodes WHERE dedupe_key=$1`, dstBy).Scan(&dst)
		if err == sql.ErrNoRows {
			return fmt.Errorf("no knowledge node %q", dstBy)
		}
		if err != nil {
			return err
		}
	}
	_, err := d.Exec(`INSERT INTO knowledge_edges (src, dst, rel) VALUES ($1,$2,$3)
ON CONFLICT (src, dst, rel) DO UPDATE SET note = EXCLUDED.note`, src, dst, rel)
	return err
}

// KnowledgeSearch: full-text over name+body with kind/tag filters.
func (d *DB) KnowledgeSearch(q, kind string, tags []string, limit int) ([]KnowledgeNode, error) {
	where := []string{"TRUE"}
	args := []any{}
	if kind != "" {
		args = append(args, kind)
		where = append(where, fmt.Sprintf("kind = $%d", len(args)))
	}
	if strings.TrimSpace(q) != "" {
		pat := "%" + strings.ToLower(strings.TrimSpace(q)) + "%"
		args = append(args, pat)
		i := len(args)
		where = append(where, fmt.Sprintf("(LOWER(name) LIKE $%d OR LOWER(body) LIKE $%d)", i, i))
	}
	if len(tags) > 0 {
		args = append(args, tags)
		where = append(where, fmt.Sprintf("tags @> $%d", len(args)))
	}
	args = append(args, limit)
	qs := `SELECT id, kind, name, COALESCE(body,''), COALESCE(refs::text,'{}'), COALESCE(tags::text,'[]'), dedupe_key, source, updated_at
FROM knowledge_nodes WHERE ` + strings.Join(where, " AND ") + ` ORDER BY updated_at DESC LIMIT $` + fmt.Sprint(len(args))
	rows, err := d.Query(qs, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanKnowledge(rows)
}

// KnowledgeNeighbors returns a node's connected nodes + the edges.
func (d *DB) KnowledgeNeighbors(id int64, relFilter string) ([]KnowledgeNode, []KnowledgeEdge, error) {
	q := `SELECT e.id, e.src, e.dst, e.rel, COALESCE(e.note,'')
FROM knowledge_edges e WHERE e.src=$1 OR e.dst=$1`
	var args []any
	if relFilter != "" {
		q += ` AND e.rel=$2`
		args = append(args, id, relFilter)
	} else {
		args = append(args, id)
	}
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var edges []KnowledgeEdge
	ids := map[int64]bool{id: true}
	for rows.Next() {
		var e KnowledgeEdge
		if err := rows.Scan(&e.ID, &e.Src, &e.Dst, &e.Rel, &e.Note); err == nil {
			edges = append(edges, e)
			ids[e.Src] = true
			ids[e.Dst] = true
		}
	}
	// fetch nodes
	var nodes []KnowledgeNode
	for nid := range ids {
		n, err := d.KnowledgeGet(nid)
		if err == nil {
			nodes = append(nodes, n)
		}
	}
	return nodes, edges, nil
}

// KnowledgeGet fetches one node by id.
func (d *DB) KnowledgeGet(id int64) (KnowledgeNode, error) {
	row := d.QueryRow(`SELECT id, kind, name, COALESCE(body,''), COALESCE(refs::text,'{}'), COALESCE(tags::text,'[]'), dedupe_key, source, updated_at
FROM knowledge_nodes WHERE id=$1`, id)
	return scanKnowledgeRow(row)
}

func scanKnowledge(rows *sql.Rows) ([]KnowledgeNode, error) {
	var out []KnowledgeNode
	for rows.Next() {
		n, err := scanKnowledgeRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func scanKnowledgeRow(sc interface{ Scan(...any) error }) (KnowledgeNode, error) {
	var n KnowledgeNode
	var refs, tags string
	if err := sc.Scan(&n.ID, &n.Kind, &n.Name, &n.Body, &refs, &tags, &n.DedupeKey, &n.Source, &n.UpdatedAt); err != nil {
		return n, err
	}
	_ = json.Unmarshal([]byte(refs), &n.Refs)
	_ = json.Unmarshal([]byte(tags), &n.Tags)
	if n.Tags == nil {
		n.Tags = []string{}
	}
	if n.Refs == nil {
		n.Refs = map[string]any{}
	}
	return n, nil
}

// KnowledgeAllForExport returns every node+edge for the Obsidian vault export.
func (d *DB) KnowledgeAllForExport() ([]KnowledgeNode, []KnowledgeEdge, error) {
	rows, err := d.Query(`SELECT id, kind, name, COALESCE(body,''), COALESCE(refs::text,'{}'), COALESCE(tags::text,'[]'), dedupe_key, source, updated_at
FROM knowledge_nodes ORDER BY kind, name`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	nodes, err := scanKnowledge(rows)
	if err != nil {
		return nil, nil, err
	}
	erows, err := d.Query(`SELECT id, src, dst, rel, COALESCE(note,'') FROM knowledge_edges ORDER BY src`)
	if err != nil {
		return nil, nil, err
	}
	defer erows.Close()
	var edges []KnowledgeEdge
	for erows.Next() {
		var e KnowledgeEdge
		if err := erows.Scan(&e.ID, &e.Src, &e.Dst, &e.Rel, &e.Note); err != nil {
			return nil, nil, err
		}
		edges = append(edges, e)
	}
	return nodes, edges, nil
}
