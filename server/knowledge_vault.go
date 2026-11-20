package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TaxMachine/ARTEX/db"
)

// knowledgeVaultExport serves the knowledge graph as an Obsidian-compatible
// markdown vault (zip): one note per node with YAML frontmatter (kind, tags,
// refs, updated), [[wikilinks]] for edges, an index note per kind, and a graph
// overview note. Humans browse/search in Obsidian; agents keep using the
// kn_search/kn_add tools on the same rows.

func (s *Server) knowledgeVaultExport(w http.ResponseWriter, r *http.Request) {
	nodes, edges, err := s.m.pg.KnowledgeAllForExport()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	byID := map[int64]db.KnowledgeNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	linkName := func(id int64) string {
		if n, ok := byID[id]; ok {
			return noteName(n)
		}
		return fmt.Sprintf("node-%d", id)
	}

	files := map[string]string{}
	for _, n := range nodes {
		var b strings.Builder
		b.WriteString("---\n")
		fmt.Fprintf(&b, "kind: %s\n", n.Kind)
		fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(n.Tags, ", "))
		fmt.Fprintf(&b, "dedupe_key: %s\n", n.DedupeKey)
		fmt.Fprintf(&b, "updated: %s\n", n.UpdatedAt.Format(time.RFC3339))
		if n.TaskID != nil {
			fmt.Fprintf(&b, "task: %d\n", *n.TaskID)
		}
		b.WriteString("---\n\n")
		fmt.Fprintf(&b, "# %s\n\n", n.Name)
		if n.Body != "" {
			b.WriteString(n.Body)
			b.WriteString("\n\n")
		}
		if len(n.Refs) > 0 {
			b.WriteString("## Refs\n\n")
			for k, v := range n.Refs {
				fmt.Fprintf(&b, "- **%s**: %v\n", k, v)
			}
			b.WriteString("\n")
		}
		var out []string
		var in []string
		for _, e := range edges {
			if e.Src == n.ID {
				out = append(out, fmt.Sprintf("- [[%s]] — %s", linkName(e.Dst), e.Rel))
			} else if e.Dst == n.ID {
				in = append(in, fmt.Sprintf("- [[%s]] — %s", linkName(e.Src), e.Rel))
			}
		}
		if len(out) > 0 {
			b.WriteString("## Links out\n\n")
			b.WriteString(strings.Join(out, "\n"))
			b.WriteString("\n\n")
		}
		if len(in) > 0 {
			b.WriteString("## Links in\n\n")
			b.WriteString(strings.Join(in, "\n"))
			b.WriteString("\n\n")
		}
		files[fmt.Sprintf("%s.md", noteName(n))] = b.String()
	}

	// per-kind index notes
	kinds := map[string][]db.KnowledgeNode{}
	for _, n := range nodes {
		kinds[n.Kind] = append(kinds[n.Kind], n)
	}
	var overview strings.Builder
	overview.WriteString("# Knowledge graph\n\n")
	for kind, ns := range kinds {
		var idx strings.Builder
		fmt.Fprintf(&idx, "# %s (%d)\n\n", kind, len(ns))
		for _, n := range ns {
			fmt.Fprintf(&idx, "- [[%s]]\n", noteName(n))
		}
		files[fmt.Sprintf("index-%s.md", kind)] = idx.String()
		fmt.Fprintf(&overview, "- [[index-%s|%s]] (%d)\n", kind, kind, len(ns))
	}
	files["000-index.md"] = overview.String()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=knowledge-vault.zip")
	writeZip(w, files)
}

func noteName(n db.KnowledgeNode) string {
	base := n.Name
	base = strings.Map(func(r rune) rune {
		switch r {
		case '#', '^', '[', ']', '|', ':', '/', '\\':
			return '-'
		}
		return r
	}, base)
	return fmt.Sprintf("%s - %s", n.Kind, base)
}

// knowledgeListNodes: the browse API for the frontend page — search + kind
// filter, same semantics as the kn_search agent tool.
func (s *Server) knowledgeListNodes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	tags := []string{}
	for _, t := range strings.Split(q.Get("tags"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, strings.ToLower(t))
		}
	}
	nodes, err := s.m.pg.KnowledgeSearch(q.Get("q"), q.Get("kind"), tags, limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"nodes": nodes})
}

// knowledgeGetNode: one node with its neighbors + edges.
func (s *Server) knowledgeGetNode(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad node id")
		return
	}
	n, err := s.m.pg.KnowledgeGet(id)
	if err != nil {
		writeErr(w, 404, "node not found")
		return
	}
	nodes, edges, _ := s.m.pg.KnowledgeNeighbors(id, "")
	writeJSON(w, 200, map[string]any{"node": n, "neighbors": nodes, "edges": edges})
}

// knowledgeStats: kind counts for the page header.
func (s *Server) knowledgeStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.m.pg.KnowledgeStats()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, stats)
}
