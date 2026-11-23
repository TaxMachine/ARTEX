package db

import (
	"context"
)

// ProjectFindingsDigest renders the compact prior-findings block injected
// deterministically into every planner/worker prompt of a project-linked task
// (the fix for "new tasks don't see same-software findings unless the model
// remembers to call project_context": the tool stays for depth, the digest
// guarantees the baseline).
//
// Compact by contract: id, class, severity, status, one-line name — capped at
// maxRows. Confirmed first (the ones that matter for "don't re-derive").
func (d *DB) ProjectFindingsDigest(ctx context.Context, projectID int64, maxRows int) (string, error) {
	if maxRows <= 0 {
		maxRows = 40
	}
	rows, err := d.QueryContext(ctx, `
		SELECT id, vulnclass, COALESCE(name, vulnclass), severity, status
		FROM findings WHERE project_id=$1
		ORDER BY CASE status WHEN 'confirmed' THEN 0 WHEN 'fixed' THEN 1 ELSE 2 END,
		         CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 ELSE 3 END,
		         id DESC
		LIMIT $2`, projectID, maxRows)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type f struct {
		id    int64
		class string
		name  string
		sev   string
		stat  string
	}
	var list []f
	for rows.Next() {
		var x f
		if err := rows.Scan(&x.id, &x.class, &x.name, &x.sev, &x.stat); err != nil {
			return "", err
		}
		list = append(list, x)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", nil
	}
	out := "[Prior findings on this software (from previous tasks in the same project) — start from these, do NOT re-derive or re-test what is already confirmed; project_context has full detail/evidence]:\n"
	for _, x := range list {
		out += "- #" + itoa64(x.id) + " " + x.class + " [" + x.sev + "/" + x.stat + "] " + clipDigest(x.name, 110) + "\n"
	}
	return out, nil
}

func clipDigest(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func itoa64(v int64) string {
	// strconv without the import dance in callers
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// ProjectFindingsDigestForTask resolves the task's project and renders its
// prior-findings digest. Empty string when the task has no project or the
// project has no findings yet.
func (d *DB) ProjectFindingsDigestForTask(ctx context.Context, taskID int64, maxRows int) (string, error) {
	p, err := d.ProjectForTask(taskID)
	if err != nil || p == nil {
		return "", err
	}
	return d.ProjectFindingsDigest(ctx, p.ID, maxRows)
}
