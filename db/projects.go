package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Project is one target software: the unit tasks on the same target
// accumulate under. Findings, binary assets (patch generations), PoCs and the
// crossmap function graph all roll up per project, so a new task on the same
// software starts from everything already known.
type Project struct {
	ID          int64     `json:"id"`
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"` // software | product | suite
	Description string    `json:"description"`
	Binaries    []string  `json:"binaries"` // crossmap binary names in this project
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

var projectKeyRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// NormalizeProjectKey slugifies a name into a project key.
func NormalizeProjectKey(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '.', r == '_':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// ErrProjectInvalid rejects a bad project key.
var ErrProjectInvalid = errors.New("project key must be 1-64 chars: lowercase letters, digits, dot, dash, underscore")

// UpsertProject creates or updates a project by key. Binaries merge (union).
// placeholderProjectName turns a slug into a clearly-pending display name for
// projects auto-created without one (title-case the slug; the operator or an
// agent is expected to save_project a proper product name + version over it).
func placeholderProjectName(key string) string {
	parts := strings.Split(key, "-")
	for i, w := range parts {
		if w == "" {
			continue
		}
		r := []rune(w)
		if r[0] >= 'a' && r[0] <= 'z' {
			r[0] = r[0] - 32
		}
		parts[i] = string(r)
	}
	return strings.Join(parts, " ") + " (unnamed product — set product name + version)"
}

func (d *DB) UpsertProject(name, key, kind, description string, binaries []string) (*Project, error) {
	key = NormalizeProjectKey(key)
	if key == "" || len(key) > 64 || !projectKeyRE.MatchString(key) {
		return nil, ErrProjectInvalid
	}
	if kind == "" {
		kind = "software"
	}
	if kind != "software" && kind != "product" && kind != "suite" {
		return nil, fmt.Errorf("project kind %q must be software|product|suite", kind)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = key
	}
	// Naming rule: the display name must be a product name + version, never a
	// bare binary/machine slug. When the caller passes nothing better than the
	// key itself (placeholder auto-create paths), keep the existing name on
	// update; on insert leave a title-cased placeholder that clearly awaits a
	// proper product name rather than freezing "msoffice-word-office-vm"-style
	// nomenclature into the UI.
	if strings.EqualFold(name, key) {
		var existing string
		_ = d.QueryRow(`SELECT name FROM projects WHERE key=$1`, key).Scan(&existing)
		if existing != "" {
			name = existing
		} else {
			name = placeholderProjectName(key)
		}
	}
	var p Project
	var bins string
	err := d.QueryRow(`
INSERT INTO projects(key, name, kind, description, binaries)
VALUES ($1,$2,$3,$4,$5::text[])
ON CONFLICT (key) DO UPDATE SET
    name        = CASE WHEN EXCLUDED.name = '' THEN projects.name ELSE EXCLUDED.name END,
    kind        = EXCLUDED.kind,
    description = CASE WHEN EXCLUDED.description = '' THEN projects.description ELSE EXCLUDED.description END,
    binaries    = (SELECT ARRAY(SELECT DISTINCT unnest(projects.binaries || EXCLUDED.binaries) ORDER BY 1)),
    updated_at  = now()
RETURNING id, key, name, kind, description, array_to_json(binaries)::text, created_at, updated_at`,
		key, name, kind, description, marshalStringArray(dedupe(binaries))).
		Scan(&p.ID, &p.Key, &p.Name, &p.Kind, &p.Description, &bins, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(bins), &p.Binaries)
	return &p, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// GetProjectByKey returns one project, nil when missing.
func (d *DB) GetProjectByKey(key string) (*Project, error) {
	key = NormalizeProjectKey(key)
	var p Project
	var bins string
	err := d.QueryRow(`SELECT id, key, name, kind, description, array_to_json(binaries)::text, created_at, updated_at
FROM projects WHERE key=$1`, key).
		Scan(&p.ID, &p.Key, &p.Name, &p.Kind, &p.Description, &bins, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(bins), &p.Binaries)
	return &p, nil
}

// ListProjects returns all projects with rollup counts (tasks, findings, binaries).
func (d *DB) ListProjects() ([]map[string]any, error) {
	rows, err := d.Query(`
SELECT p.id, p.key, p.name, p.kind, p.description, array_to_json(p.binaries)::text, p.created_at, p.updated_at,
       (SELECT count(*) FROM tasks t WHERE t.project_id=p.id AND t.deleted_at IS NULL AND t.status='running'),
       (SELECT count(*) FROM tasks t WHERE t.project_id=p.id AND t.deleted_at IS NULL AND t.status IN ('done','failed','timeout')),
       (SELECT count(*) FROM findings f WHERE f.project_id=p.id),
       (SELECT count(*) FROM findings f WHERE f.project_id=p.id AND f.severity='critical'),
       (SELECT count(*) FROM findings f WHERE f.project_id=p.id AND f.severity='high'),
       (SELECT count(*) FROM findings f WHERE f.project_id=p.id AND f.severity='medium'),
       (SELECT count(*) FROM findings f WHERE f.project_id=p.id AND f.severity='low'),
       (SELECT count(*) FROM assets a WHERE a.project_id=p.id)
FROM projects p ORDER BY p.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var key, name, kind, desc, bins string
		var created, updated time.Time
		var tasksRun, tasksDone, findings, fc, fh, fm, fl, assets int64
		if err := rows.Scan(&id, &key, &name, &kind, &desc, &bins, &created, &updated,
			&tasksRun, &tasksDone, &findings, &fc, &fh, &fm, &fl, &assets); err != nil {
			return nil, err
		}
		var binList []string
		_ = json.Unmarshal([]byte(bins), &binList)
		out = append(out, map[string]any{
			"id": id, "key": key, "name": name, "kind": kind, "description": desc,
			"binaries": binList, "created_at": created, "updated_at": updated,
			"task_count": tasksRun + tasksDone, "tasks_running": tasksRun, "tasks_done": tasksDone,
			"finding_count": findings, "findings": map[string]int64{"critical": fc, "high": fh, "medium": fm, "low": fl},
			"asset_count": assets,
		})
	}
	return out, rows.Err()
}

// SetTaskProject links a task to a project (0 = unlink).
func (d *DB) SetTaskProject(taskID, projectID int64) error {
	_, err := d.Exec(`UPDATE tasks SET project_id=NULLIF($2,0), updated_at=now() WHERE id=$1`, taskID, projectID)
	return err
}

// ProjectForTask returns the project of a task (nil when none).
func (d *DB) ProjectForTask(taskID int64) (*Project, error) {
	var key string
	err := d.QueryRow(`SELECT p.key FROM projects p JOIN tasks t ON t.project_id=p.id WHERE t.id=$1`, taskID).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d.GetProjectByKey(key)
}

// AutoLinkProject links a task to a project when its goal/description names
// one of the project's binaries (or the project key). First match wins; no
// match leaves the task unlinked. Called on task creation.
func (d *DB) AutoLinkProject(taskID int64, goal, description string) (string, error) {
	rows, err := d.Query(`SELECT id, key, array_to_json(binaries)::text FROM projects`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type cand struct {
		id  int64
		key string
		bin []string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		var bins string
		if err := rows.Scan(&c.id, &c.key, &bins); err != nil {
			return "", err
		}
		_ = json.Unmarshal([]byte(bins), &c.bin)
		cands = append(cands, c)
	}
	text := strings.ToLower(goal + " " + description)
	for _, c := range cands {
		for _, b := range c.bin {
			if b != "" && strings.Contains(text, strings.ToLower(b)) {
				if err := d.SetTaskProject(taskID, c.id); err != nil {
					return "", err
				}
				return c.key, nil
			}
		}
	}
	return "", nil
}
