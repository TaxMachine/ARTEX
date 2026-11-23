package server

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"github.com/klauspost/compress/zstd"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/TaxMachine/ARTEX/rsi"
)

// ExportTaskArchiveWorld builds a Dream-RSI replay world directly from an
// archived task package (task-<archiveID>-<taskID>.tar.zst) — the path for
// history whose live exploration rows were deleted at archive time (task 1
// predates the auto-export hook). The archive manifest's tables carry the
// full exploration_nodes/edges JSON.
func (s *Server) ExportTaskArchiveWorld(ctx context.Context, archiveID int64) (map[string]any, error) {
	var path string
	if err := s.m.pg.QueryRowContext(ctx,
		`SELECT archive_path FROM task_archives WHERE id=$1`, archiveID).Scan(&path); err != nil {
		return nil, fmt.Errorf("archive %d: %w", archiveID, err)
	}
	if !strings.HasPrefix(path, "/") {
		path = filepath.Join(s.m.dir, strings.TrimPrefix(path, "/app/data/"))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	tr := tar.NewReader(zr.IOReadCloser())

	var nodes []archiveNode
	var edges []archiveEdge
	var taskID int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(hdr.Name, "manifest.json") {
			// The manifest is a full archive snapshot (task 1's is 160MB).
			// Spill it to a temp file and scan that — the proven path: direct
			// streaming json.Decoder over the tar reader truncated mid-manifest
			// in-container even though the same bytes decode fine from a file.
			tmp, terr := os.CreateTemp("", "rsi-manifest-*.json")
			if terr != nil {
				return nil, terr
			}
			tmpName := tmp.Name()
			defer os.Remove(tmpName)
			if _, terr = io.Copy(tmp, io.LimitReader(tr, 512<<20)); terr != nil {
				tmp.Close()
				return nil, fmt.Errorf("manifest spill: %w", terr)
			}
			tmp.Close()
			mf, merr := os.Open(tmpName)
			if merr != nil {
				return nil, merr
			}
			tid, n, e, derr := scanManifestForExploration(json.NewDecoder(mf))
			mf.Close()
			if derr != nil {
				return nil, fmt.Errorf("manifest scan: %w", derr)
			}
			taskID = tid
			if len(n) > 0 {
				nodes = n
			}
			if len(e) > 0 {
				edges = e
			}
			continue
		}
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("archive %d carries no exploration rows (export before archiving, or the task had none)", archiveID)
	}

	// Build the world with the same evaluator as live export.
	parent := map[int64]int64{}
	childCount := map[int64]int{}
	for _, e := range edges {
		if e.Rel == "derived_from" || e.Rel == "spawns" {
			if _, seen := parent[e.DstID]; !seen {
				parent[e.DstID] = e.SrcID
			}
			childCount[e.SrcID]++
		}
	}
	w := rsi.World{TaskID: taskID, Name: fmt.Sprintf("archive%d-task%d", archiveID, taskID)}
	for _, n := range nodes {
		q, _ := rsi.EvaluateNode(n.Kind, n.Payload, childCount[n.ID], 0)
		w.Nodes = append(w.Nodes, rsi.Node{ID: n.ID, Kind: n.Kind, Parent: parent[n.ID], Score: q})
		if n.Kind == "goal" && w.Root == 0 {
			w.Root = n.ID
		}
	}
	if w.Root == 0 && len(nodes) > 0 {
		w.Root = nodes[0].ID
	}
	for i := range w.Nodes {
		if w.Nodes[i].Parent == 0 && w.Nodes[i].ID != w.Root {
			w.Nodes[i].Parent = w.Root
		}
	}
	w.B = len(w.Nodes)
	for _, n := range w.Nodes {
		if n.Score > w.S {
			w.S = n.Score
		}
	}
	tree, _ := json.Marshal(w)
	stats, _ := json.Marshal(map[string]any{"B": w.B, "S": w.S, "from_archive": archiveID})
	var id int64
	err = s.m.pg.QueryRowContext(ctx, `
		INSERT INTO rsi_worlds (task_id, name, tree, stats)
		VALUES ($1,$2,$3::jsonb,$4::jsonb)
		ON CONFLICT (task_id, name) DO UPDATE SET tree=EXCLUDED.tree, stats=EXCLUDED.stats
		RETURNING id`, w.TaskID, w.Name, string(tree), string(stats)).Scan(&id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"world_id": id, "task_id": w.TaskID, "name": w.Name, "nodes": w.B, "best": w.S}, nil
}

// rsiExportArchiveWorld: POST /api/rsi/export-archive/{archiveID}.
func (s *Server) rsiExportArchiveWorld(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "invalid archive id")
		return
	}
	out, err := s.ExportTaskArchiveWorld(r.Context(), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// scanManifestForExploration walks the manifest's top level and returns at the
// tables object (proven shape; consuming the remainder of the 160MB manifest
// after tables caused a spurious EOF on real archives).
func scanManifestForExploration(dec *json.Decoder) (int64, []archiveNode, []archiveEdge, error) {
	var taskID int64
	if _, err := dec.Token(); err != nil {
		return 0, nil, nil, err
	}
	var nodes []archiveNode
	var edges []archiveEdge
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return 0, nil, nil, err
		}
		k, _ := kt.(string)
		switch k {
		case "task_id":
			if err := dec.Decode(&taskID); err != nil {
				return 0, nil, nil, err
			}
		case "tables":
			if err := scanTables(dec, &nodes, &edges); err != nil {
				return 0, nil, nil, err
			}
			return taskID, nodes, edges, nil
		default:
			if err := dec.Decode(new(json.RawMessage)); err != nil {
				return 0, nil, nil, err
			}
		}
	}
	return taskID, nodes, edges, nil
}

func scanTables(dec *json.Decoder, nodes *[]archiveNode, edges *[]archiveEdge) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("tables is not an object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := keyTok.(string)
		switch key {
		case "exploration_nodes":
			if err := dec.Decode(nodes); err != nil {
				return fmt.Errorf("nodes: %w", err)
			}
		case "exploration_edges":
			if err := dec.Decode(edges); err != nil {
				return fmt.Errorf("edges: %w", err)
			}
		default:
			if err := dec.Decode(new(json.RawMessage)); err != nil {
				return err
			}
		}
	}
	_, _ = dec.Token()
	return nil
}

type archiveNode struct {
	ID      int64          `json:"id"`
	Kind    string         `json:"kind"`
	Payload map[string]any `json:"payload"`
}

type archiveEdge struct {
	SrcID int64  `json:"src_id"`
	DstID int64  `json:"dst_id"`
	Rel   string `json:"rel"`
}
