package db

import (
	"context"
	"time"
)

// Platform issues: the operator's backlog of ARTEX defects surfaced by the
// self-improvement loops. Deliberately separate from rsi_lessons — a platform
// bug is work for the operator, not context for the workers.

type PlatformIssue struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail"`
	Source    string    `json:"source"`
	SourceRef string    `json:"source_ref"`
	Status    string    `json:"status"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreatePlatformIssue files a new issue; duplicate titles from the same source
// are collapsed (ON CONFLICT would need a key — a soft dedupe here).
func (d *DB) CreatePlatformIssue(ctx context.Context, title, detail, source, sourceRef string) (int64, bool, error) {
	title = clipString(title, 200)
	detail = clipString(detail, 4000)
	var existing int64
	err := d.QueryRowContext(ctx, `
		SELECT id FROM platform_issues WHERE title=$1 AND source=$2 AND status='open' LIMIT 1`,
		title, source).Scan(&existing)
	if err == nil {
		// already filed and still open: append the new sighting to the detail
		_, _ = d.ExecContext(ctx, `UPDATE platform_issues SET updated_at=now() WHERE id=$1`, existing)
		return existing, false, nil
	}
	var id int64
	err = d.QueryRowContext(ctx, `
		INSERT INTO platform_issues (title, detail, source, source_ref)
		VALUES ($1,$2,$3,$4) RETURNING id`,
		title, detail, source, sourceRef).Scan(&id)
	return id, true, err
}

// ListPlatformIssues: status filter (empty = all), newest first.
func (d *DB) ListPlatformIssues(ctx context.Context, status string, limit int) ([]PlatformIssue, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := `SELECT id, title, detail, source, source_ref, status, note, created_at, updated_at FROM platform_issues`
	args := []any{}
	if status != "" {
		q += ` WHERE status=$1`
		args = append(args, status)
	}
	q += ` ORDER BY CASE status WHEN 'open' THEN 0 ELSE 1 END, updated_at DESC LIMIT ` + itoa(int64(limit))
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlatformIssue
	for rows.Next() {
		var p PlatformIssue
		if err := rows.Scan(&p.ID, &p.Title, &p.Detail, &p.Source, &p.SourceRef, &p.Status, &p.Note, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdatePlatformIssueStatus resolves/reopens with a note.
func (d *DB) UpdatePlatformIssueStatus(ctx context.Context, id int64, status, note string) error {
	_, err := d.ExecContext(ctx, `
		UPDATE platform_issues SET status=$2, note=$3, updated_at=now() WHERE id=$1`,
		id, status, clipString(note, 2000))
	return err
}

func clipString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
