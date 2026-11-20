package server

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/TaxMachine/ARTEX/db"
)

// PoC packaging for the findings evidence zip: machine pcaps (capture_* tool
// rows owned by the finding's task), so a downloaded finding carries its full
// proof bundle — report + HTTP traffic + packet captures. Handler transcripts
// (msf) live in the task activity log and are referenced from the report
// markdown; their files are task-scoped scratch, not durable evidence.

// exportPG is the server's PG handle for the findings export (set at startup).
var exportPG *db.DB

// SetFindingsExportPG installs the DB handle used by the pcap bundling.
func SetFindingsExportPG(d *db.DB) { exportPG = d }

// writeFindingPcaps appends every finished pcap of the finding's task to the
// archive under evidence/<finding>/pcaps/. LocalPath is the converted .pcap on
// the ARTEX host (etl2pcap already applied); missing files are skipped.
func writeFindingPcaps(zw *zip.Writer, f *db.DBFinding, pg *db.DB) {
	if f.TaskID == nil || pg == nil {
		return
	}
	caps, err := pg.Machines().CapturesForTask(*f.TaskID, 20)
	if err != nil {
		return
	}
	for _, c := range caps {
		if c.LocalPath == "" {
			continue
		}
		src, err := os.Open(c.LocalPath)
		if err != nil {
			continue
		}
		entry := "evidence/" + strconv.FormatInt(f.ID, 10) + "/pcaps/" + filepath.Base(c.LocalPath)
		w, err := zw.Create(entry)
		if err != nil {
			src.Close()
			continue
		}
		_, _ = io.Copy(w, src)
		src.Close()
	}
}

// pocDownload streams a finding's ready PoC zip.
func (s *Server) pocDownload(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad finding id")
		return
	}
	f, err := s.m.pg.GetFinding(id)
	if err != nil {
		writeErr(w, 404, "finding not found")
		return
	}
	if f.PoCStatus != "ready" || f.PoCPath == "" {
		writeErr(w, 409, "no PoC package on this finding (status "+f.PoCStatus+")")
		return
	}
	file, err := os.Open(f.PoCPath)
	if err != nil {
		writeErr(w, 500, "PoC zip missing on the server: "+err.Error())
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"poc-finding-%d.zip\"", id))
	_, _ = io.Copy(w, file)
}
