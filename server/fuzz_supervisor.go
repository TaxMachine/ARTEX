package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// fuzzSupervisor: a background loop (5 min tick) that babysits detached fuzz
// campaigns on active tasks. When a campaign reports NEW unique crash
// signatures (crashes/index.txt grew), it enqueues ONE high-priority triage
// intent on the owning task, so the crash backlog is worked without a human
// noticing. Deliberately conservative: never kills or restarts campaigns (the
// VM is single-tenant and agents manage their own runs), never duplicates
// dispatches for the same growth, only acts on non-terminal tasks.

type fuzzSupervisor struct {
	s        *Server
	lastSeen map[string]int // taskID|runDir -> unique-crash count last tick
}

func (f *fuzzSupervisor) run(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.tick(ctx)
		}
	}
}

func (f *fuzzSupervisor) tick(ctx context.Context) {
	for _, t := range f.s.m.List() {
		if t == nil {
			continue
		}
		f.superviseTask(ctx, t)
	}
}

func (f *fuzzSupervisor) superviseTask(ctx context.Context, t *Task) {
	rows, err := f.s.m.pg.Query(`SELECT summary FROM activity
WHERE exploration_id = $1 AND tool = 'fuzz_campaign' AND kind = 'tool_use'
ORDER BY id DESC LIMIT 10`, t.ID)
	if err != nil {
		return
	}
	defer rows.Close()
	var runs []string
	for rows.Next() {
		var sum string
		if rows.Scan(&sum) == nil {
			var in struct {
				Out string `json:"out"`
			}
			// the summary holds the tool-use arguments JSON; out is the run dir
			if i := strings.Index(sum, "{"); i >= 0 && json.Unmarshal([]byte(sum[i:]), &in) == nil && in.Out != "" {
				runs = append(runs, in.Out)
			}
		}
	}
	for _, dir := range runs {
		f.superviseRun(ctx, t, dir)
	}
}

func (f *fuzzSupervisor) superviseRun(ctx context.Context, t *Task, dir string) {
	key := t.ID + "|" + dir
	out, err := f.s.m.Machines().Exec(ctx, parseTaskIDSafe(t.ID), "office-vm", "", 0,
		psExecReadIndex(dir), 20*time.Second)
	if err != nil {
		return
	}
	lines := 0
	for _, l := range strings.Split(out.Stdout, "\n") {
		if strings.TrimSpace(l) != "" && strings.Contains(l, "\t") {
			lines++
		}
	}
	prev, seen := f.lastSeen[key]
	f.lastSeen[key] = lines
	if !seen || lines <= prev {
		return
	}
	summary := fmt.Sprintf("Triage %d new fuzz crash signature(s) from %s (auto-dispatched by the fuzz supervisor): fuzz_campaign action=crashes to list them, then crash_minimize + crash_triage_auto on each unique crashing input; record each primitive as a fact AND kn_add it to the knowledge graph (kind=crash_signature / primitive)", lines-prev, dir)
	if _, err := t.Store.AddIntent(map[string]any{"summary": summary}, 8, nil, "fuzz-supervisor"); err != nil {
		log.Printf("[fuzz-supervisor] task %s triage intent failed: %v", t.ID, err)
		return
	}
	t.Notify() // wake the planner
	log.Printf("[fuzz-supervisor] task %s: %d new crash signature(s) -> triage intent dispatched", t.ID, lines-prev)
}

func psExecReadIndex(dir string) string {
	ps := fmt.Sprintf(`if (Test-Path '%s\crashes\index.txt') { Get-Content '%s\crashes\index.txt' } else { Write-Output EMPTY }`, dir, dir)
	u16 := make([]byte, 0, len(ps)*2)
	for _, b := range []byte(ps) {
		u16 = append(u16, b, 0)
	}
	return "powershell -NoProfile -EncodedCommand " + base64.StdEncoding.EncodeToString(u16)
}
