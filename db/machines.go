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

// Machine is an operator-registered VM/SSH target. Agents address it by name;
// the connection fields and scripts are never taken from agent input.
type Machine struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	OS          string         `json:"os"`
	Kind        string         `json:"kind"` // libvirt | script | ssh
	Host        string         `json:"host"`
	SSHPort     int            `json:"ssh_port"`
	Domain      string         `json:"domain"`
	StartScript string         `json:"start_script,omitempty"`
	StopScript  string         `json:"stop_script,omitempty"`
	Workdir     string         `json:"workdir,omitempty"`
	DataDir     string         `json:"data_dir,omitempty"`
	Protected   bool           `json:"protected"`
	Tags        []string       `json:"tags"`
	Config      map[string]any `json:"config,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// MachineEvent is one recorded machine action (SSH exec/upload/download, VM
// lifecycle, capture control). It is the machine equivalent of a traffic record.
type MachineEvent struct {
	ID           int64          `json:"id"`
	TaskID       *int64         `json:"task_id,omitempty"`
	MachineID    *int64         `json:"machine_id,omitempty"`
	Machine      string         `json:"machine"`
	Protocol     string         `json:"protocol"` // ssh | vm | pcap
	Action       string         `json:"action"`
	Username     string         `json:"username,omitempty"`
	CredentialID *int64         `json:"credential_id,omitempty"`
	Command      string         `json:"command,omitempty"`
	Stdout       string         `json:"stdout,omitempty"`
	Stderr       string         `json:"stderr,omitempty"`
	ExitCode     *int           `json:"exit_code,omitempty"`
	DurationMS   int            `json:"duration_ms"`
	Bytes        int64          `json:"bytes"`
	PcapPath     string         `json:"pcap_path,omitempty"`
	Meta         map[string]any `json:"meta,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

// MachineCapture is one pcap capture started through SSH.
type MachineCapture struct {
	ID         int64      `json:"id"`
	MachineID  int64      `json:"machine_id"`
	TaskID     *int64     `json:"task_id,omitempty"`
	Iface      string     `json:"iface"`
	BPFFilter  string     `json:"bpf_filter"`
	RemotePath string     `json:"remote_path"`
	LocalPath  string     `json:"local_path"`
	PIDFile    string     `json:"pid_file"`
	Status     string     `json:"status"`
	Bytes      int64      `json:"bytes"`
	StartedAt  time.Time  `json:"started_at"`
	StoppedAt  *time.Time `json:"stopped_at,omitempty"`
}

var machineNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

func (m Machine) Validate() error {
	m.Name = strings.TrimSpace(m.Name)
	if !machineNameRE.MatchString(m.Name) {
		return errors.New("machine name must be 1-64 chars: letters, digits, dot, dash, underscore")
	}
	switch m.Kind {
	case "libvirt", "script", "ssh":
	default:
		return fmt.Errorf("machine kind %q must be libvirt|script|ssh", m.Kind)
	}
	if m.Kind == "libvirt" && strings.TrimSpace(m.Domain) == "" {
		return errors.New("libvirt machines need a domain name")
	}
	if m.Kind == "script" && (m.StartScript == "" || m.StopScript == "") {
		return errors.New("script machines need start_script and stop_script")
	}
	if m.SSHPort < 0 || m.SSHPort > 65535 {
		return errors.New("ssh_port must be between 0 and 65535")
	}
	return nil
}

// Machines returns the machine store.
func (d *DB) Machines() *MachineStore { return &MachineStore{db: d} }

// MachineStore operates on the machines/machine_events/machine_captures tables.
type MachineStore struct{ db *DB }

func scanMachine(row interface{ Scan(...any) error }) (Machine, error) {
	var m Machine
	var tagsRaw, cfgRaw []byte
	err := row.Scan(&m.ID, &m.Name, &m.Description, &m.OS, &m.Kind, &m.Host, &m.SSHPort,
		&m.Domain, &m.StartScript, &m.StopScript, &m.Workdir, &m.DataDir,
		&m.Protected, &tagsRaw, &cfgRaw, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return m, err
	}
	if len(tagsRaw) > 0 {
		_ = json.Unmarshal(tagsRaw, &m.Tags)
	}
	if len(cfgRaw) > 0 {
		_ = json.Unmarshal(cfgRaw, &m.Config)
	}
	return m, nil
}

const machineCols = `id, name, description, os, kind, host, ssh_port, domain,
	start_script, stop_script, workdir, data_dir, protected, tags, config, created_at, updated_at`

func (s *MachineStore) List() ([]Machine, error) {
	rows, err := s.db.Query(`SELECT ` + machineCols + ` FROM machines ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Machine
	for rows.Next() {
		m, err := scanMachine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *MachineStore) Get(name string) (Machine, error) {
	return scanMachine(s.db.QueryRow(`SELECT `+machineCols+` FROM machines WHERE name=$1`, strings.TrimSpace(name)))
}

func (s *MachineStore) GetByID(id int64) (Machine, error) {
	return scanMachine(s.db.QueryRow(`SELECT `+machineCols+` FROM machines WHERE id=$1`, id))
}

func (s *MachineStore) Create(m Machine) (Machine, error) {
	m.Name = strings.TrimSpace(m.Name)
	m.Kind = strings.ToLower(strings.TrimSpace(m.Kind))
	if err := m.Validate(); err != nil {
		return Machine{}, err
	}
	if m.SSHPort == 0 {
		m.SSHPort = 22
	}
	tags, _ := json.Marshal(m.Tags)
	if len(tags) == 0 || string(tags) == "null" {
		tags = []byte(`[]`)
	}
	cfg, _ := json.Marshal(m.Config)
	if len(cfg) == 0 || string(cfg) == "null" {
		cfg = []byte(`{}`)
	}
	row := s.db.QueryRow(`
		INSERT INTO machines (name, description, os, kind, host, ssh_port, domain,
			start_script, stop_script, workdir, data_dir, protected, tags, config)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING `+machineCols,
		m.Name, m.Description, m.OS, m.Kind, m.Host, m.SSHPort, m.Domain,
		m.StartScript, m.StopScript, m.Workdir, m.DataDir, m.Protected, tags, string(cfg))
	return scanMachine(row)
}

func (s *MachineStore) Update(m Machine) (Machine, error) {
	m.Name = strings.TrimSpace(m.Name)
	m.Kind = strings.ToLower(strings.TrimSpace(m.Kind))
	if err := m.Validate(); err != nil {
		return Machine{}, err
	}
	tags, _ := json.Marshal(m.Tags)
	if len(tags) == 0 || string(tags) == "null" {
		tags = []byte(`[]`)
	}
	cfg, _ := json.Marshal(m.Config)
	if len(cfg) == 0 || string(cfg) == "null" {
		cfg = []byte(`{}`)
	}
	row := s.db.QueryRow(`
		UPDATE machines SET description=$2, os=$3, kind=$4, host=$5, ssh_port=$6, domain=$7,
			start_script=$8, stop_script=$9, workdir=$10, data_dir=$11, protected=$12,
			tags=$13, config=$14, updated_at=now()
		WHERE id=$1 RETURNING `+machineCols,
		m.ID, m.Description, m.OS, m.Kind, m.Host, m.SSHPort, m.Domain,
		m.StartScript, m.StopScript, m.Workdir, m.DataDir, m.Protected, tags, string(cfg))
	return scanMachine(row)
}

func (s *MachineStore) Delete(id int64) error {
	tag, err := s.db.Exec(`DELETE FROM machines WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := tag.RowsAffected(); n == 0 {
		return errors.New("machine not found")
	}
	return nil
}

// ---------------------------------------------------------------- events ---

// RecordEvent inserts one machine event and returns its id. Text is capped by
// the caller; the store keeps whatever it is given.
func (s *MachineStore) RecordEvent(e MachineEvent) (int64, error) {
	meta, _ := json.Marshal(e.Meta)
	if len(meta) == 0 || string(meta) == "null" {
		meta = []byte(`{}`)
	}
	var id int64
	err := s.db.QueryRow(`
		INSERT INTO machine_events (task_id, machine_id, machine, protocol, action, username,
			credential_id, command, stdout, stderr, exit_code, duration_ms, bytes, pcap_path, meta)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		RETURNING id`,
		e.TaskID, e.MachineID, e.Machine, e.Protocol, e.Action, e.Username,
		e.CredentialID, e.Command, e.Stdout, e.Stderr, e.ExitCode, e.DurationMS, e.Bytes,
		e.PcapPath, string(meta)).Scan(&id)
	return id, err
}

// MachineEventFilter narrows an event listing. TaskID>0 is mandatory for agent
// callers; the UI may pass 0 to see everything.
type MachineEventFilter struct {
	TaskID  int64
	Machine string
	Action  string
	Query   string // substring over command/stdout/stderr
	Limit   int
	Offset  int
}

func (s *MachineStore) Events(f MachineEventFilter) ([]MachineEvent, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 25
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	args := []any{}
	where := []string{"1=1"}
	if f.TaskID > 0 {
		args = append(args, f.TaskID)
		where = append(where, fmt.Sprintf("task_id=$%d", len(args)))
	}
	if f.Machine != "" {
		args = append(args, f.Machine)
		where = append(where, fmt.Sprintf("machine=$%d", len(args)))
	}
	if f.Action != "" {
		args = append(args, f.Action)
		where = append(where, fmt.Sprintf("action=$%d", len(args)))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+q+"%")
		where = append(where, fmt.Sprintf("(command ILIKE $%d OR stdout ILIKE $%d OR stderr ILIKE $%d)",
			len(args), len(args), len(args)))
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.db.Query(`
		SELECT id, task_id, machine_id, machine, protocol, action, username, credential_id,
		       command, stdout, stderr, exit_code, duration_ms, bytes, pcap_path, meta, created_at
		FROM machine_events WHERE `+strings.Join(where, " AND ")+`
		ORDER BY id DESC LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineEvent
	for rows.Next() {
		var e MachineEvent
		var metaRaw []byte
		var taskID, machineID, credID, exitCode sql.NullInt64
		if err := rows.Scan(&e.ID, &taskID, &machineID, &e.Machine, &e.Protocol, &e.Action,
			&e.Username, &credID, &e.Command, &e.Stdout, &e.Stderr, &exitCode,
			&e.DurationMS, &e.Bytes, &e.PcapPath, &metaRaw, &e.CreatedAt); err != nil {
			return nil, err
		}
		if taskID.Valid {
			v := taskID.Int64
			e.TaskID = &v
		}
		if machineID.Valid {
			v := machineID.Int64
			e.MachineID = &v
		}
		if credID.Valid {
			v := credID.Int64
			e.CredentialID = &v
		}
		if exitCode.Valid {
			v := int(exitCode.Int64)
			e.ExitCode = &v
		}
		if len(metaRaw) > 0 {
			_ = json.Unmarshal(metaRaw, &e.Meta)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *MachineStore) Event(id, taskID int64) (MachineEvent, error) {
	args := []any{id}
	q := `SELECT id, task_id, machine_id, machine, protocol, action, username, credential_id,
	             command, stdout, stderr, exit_code, duration_ms, bytes, pcap_path, meta, created_at
	      FROM machine_events WHERE id=$1`
	if taskID > 0 {
		args = append(args, taskID)
		q += ` AND task_id=$2`
	}
	var e MachineEvent
	var metaRaw []byte
	var taskIDv, machineID, credID, exitCode sql.NullInt64
	err := s.db.QueryRow(q, args...).Scan(&e.ID, &taskIDv, &machineID, &e.Machine, &e.Protocol,
		&e.Action, &e.Username, &credID, &e.Command, &e.Stdout, &e.Stderr, &exitCode,
		&e.DurationMS, &e.Bytes, &e.PcapPath, &metaRaw, &e.CreatedAt)
	if err != nil {
		return e, err
	}
	if taskIDv.Valid {
		v := taskIDv.Int64
		e.TaskID = &v
	}
	if machineID.Valid {
		v := machineID.Int64
		e.MachineID = &v
	}
	if credID.Valid {
		v := credID.Int64
		e.CredentialID = &v
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		e.ExitCode = &v
	}
	if len(metaRaw) > 0 {
		_ = json.Unmarshal(metaRaw, &e.Meta)
	}
	return e, nil
}

// --------------------------------------------------------------- captures ---

func (s *MachineStore) CreateCapture(c MachineCapture) (int64, error) {
	var id int64
	err := s.db.QueryRow(`
		INSERT INTO machine_captures (machine_id, task_id, iface, bpf_filter, remote_path, pid_file)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		c.MachineID, c.TaskID, c.Iface, c.BPFFilter, c.RemotePath, c.PIDFile).Scan(&id)
	return id, err
}

func (s *MachineStore) FinishCapture(id int64, localPath string, bytes int64, status string) error {
	_, err := s.db.Exec(`
		UPDATE machine_captures SET local_path=$2, bytes=$3, status=$4, stopped_at=now()
		WHERE id=$1`, id, localPath, bytes, status)
	return err
}

func (s *MachineStore) Capture(id int64) (MachineCapture, error) {
	var c MachineCapture
	var taskID sql.NullInt64
	var stopped sql.NullTime
	err := s.db.QueryRow(`
		SELECT id, machine_id, task_id, iface, bpf_filter, remote_path, local_path, pid_file,
		       status, bytes, started_at, stopped_at
		FROM machine_captures WHERE id=$1`, id).
		Scan(&c.ID, &c.MachineID, &taskID, &c.Iface, &c.BPFFilter, &c.RemotePath,
			&c.LocalPath, &c.PIDFile, &c.Status, &c.Bytes, &c.StartedAt, &stopped)
	if err != nil {
		return c, err
	}
	if taskID.Valid {
		v := taskID.Int64
		c.TaskID = &v
	}
	if stopped.Valid {
		v := stopped.Time
		c.StoppedAt = &v
	}
	return c, nil
}

// RecentCaptures lists the newest captures across every machine, with the
// machine name joined in for the packet-captures page.
func (s *MachineStore) RecentCaptures(limit int) ([]MachineCaptureWithMachine, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT c.id, c.machine_id, m.name, c.task_id, c.iface, c.bpf_filter,
		       c.remote_path, c.local_path, c.pid_file, c.status, c.bytes,
		       c.started_at, c.stopped_at
		FROM machine_captures c JOIN machines m ON m.id = c.machine_id
		ORDER BY c.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineCaptureWithMachine
	for rows.Next() {
		var c MachineCaptureWithMachine
		var stopped sql.NullTime
		if err := rows.Scan(&c.ID, &c.MachineID, &c.Machine, &c.TaskID, &c.Iface, &c.BPFFilter,
			&c.RemotePath, &c.LocalPath, &c.PIDFile, &c.Status, &c.Bytes, &c.StartedAt, &stopped); err != nil {
			return nil, err
		}
		if stopped.Valid {
			t := stopped.Time
			c.StoppedAt = &t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MachineCaptureWithMachine is a capture row plus its machine's name.
type MachineCaptureWithMachine struct {
	MachineCapture
	Machine string `json:"machine"`
}

func (s *MachineStore) Captures(machineID int64, limit int) ([]MachineCapture, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(`
		SELECT id, machine_id, task_id, iface, bpf_filter, remote_path, local_path, pid_file,
		       status, bytes, started_at, stopped_at
		FROM machine_captures WHERE machine_id=$1 ORDER BY id DESC LIMIT $2`, machineID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineCapture
	for rows.Next() {
		var c MachineCapture
		var taskID sql.NullInt64
		var stopped sql.NullTime
		if err := rows.Scan(&c.ID, &c.MachineID, &taskID, &c.Iface, &c.BPFFilter, &c.RemotePath,
			&c.LocalPath, &c.PIDFile, &c.Status, &c.Bytes, &c.StartedAt, &stopped); err != nil {
			return nil, err
		}
		if taskID.Valid {
			v := taskID.Int64
			c.TaskID = &v
		}
		if stopped.Valid {
			v := stopped.Time
			c.StoppedAt = &v
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ----------------------------------------------------------- task links ---

// LinkTaskMachine records that a task used a machine. Best-effort callers:
// a failed link must never fail the tool call.
func (s *MachineStore) LinkTaskMachine(taskID, machineID int64) error {
	if taskID <= 0 || machineID <= 0 {
		return nil
	}
	_, err := s.db.Exec(`
		INSERT INTO task_machines (task_id, machine_id) VALUES ($1, $2)
		ON CONFLICT (task_id, machine_id) DO UPDATE SET updated_at = now()`,
		taskID, machineID)
	return err
}

// TaskMachines returns the machines linked to one task (autostart-eligible only).
func (s *MachineStore) TaskMachines(taskID int64) ([]Machine, error) {
	rows, err := s.db.Query(`
		SELECT m.id, m.name, m.description, m.os, m.kind, m.host, m.ssh_port, m.domain,
		       m.start_script, m.stop_script, m.workdir, m.data_dir, m.protected, m.tags,
		       m.config, m.created_at, m.updated_at
		FROM machines m
		JOIN task_machines tm ON tm.machine_id = m.id AND tm.autostart
		WHERE tm.task_id = $1
		ORDER BY m.name`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Machine
	for rows.Next() {
		m, err := scanMachine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AutostartCandidates returns the distinct machines used by tasks that are
// alive, not paused, already started, and not terminal. A machine can opt out
// with config.autostart=false.
func (s *MachineStore) AutostartCandidates() ([]Machine, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT m.id, m.name, m.description, m.os, m.kind, m.host, m.ssh_port, m.domain,
		       m.start_script, m.stop_script, m.workdir, m.data_dir, m.protected, m.tags,
		       m.config, m.created_at, m.updated_at
		FROM machines m
		JOIN task_machines tm ON tm.machine_id = m.id AND tm.autostart
		JOIN tasks t ON t.id = tm.task_id
		WHERE t.archived_at IS NULL
		  AND t.deleted_at IS NULL
		  AND t.paused = false
		  AND t.status NOT IN ('done', 'failed', 'timeout')
		  AND COALESCE((m.config->>'autostart')::boolean, true)
		ORDER BY m.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Machine
	for rows.Next() {
		m, err := scanMachine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
