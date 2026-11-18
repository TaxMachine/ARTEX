package db

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// TaskCredential is a per-task account: a web login (protocol=web, the original
// #3 feature), an SSH login (protocol=ssh), or WinRM. Either declared by the user
// at task creation (source=user) or registered by the agent itself (source=agent).
//
// Passwords and SSH private keys are encrypted at rest (secrets.go) and masked by
// the HTTP API; the agent tools read them decrypted. Anything injected into agent
// context transits to the LLM provider, so only dedicated test accounts belong here.
type TaskCredential struct {
	ID         int64     `json:"id"`
	TaskID     int64     `json:"task_id"`
	Protocol   string    `json:"protocol"` // web | ssh | winrm
	Domain     string    `json:"domain"`   // host/IP or web domain
	Port       int       `json:"port"`     // 0 = protocol default
	Username   string    `json:"username"`
	Password   string    `json:"password"`
	SecretType string    `json:"secret_type"` // password | key
	PrivateKey string    `json:"private_key,omitempty"`
	Source     string    `json:"source"` // "user" | "agent"
	Note       string    `json:"note,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TaskCredentialInput validates and normalizes one upsert. Empty username or
// domain is rejected. An agent must supply the secret it actually used; user
// rows may be placeholders that an agent fills in later.
type TaskCredentialInput struct {
	Protocol   string
	Domain     string
	Port       int
	Username   string
	Password   string
	SecretType string
	PrivateKey string
	Source     string
	Note       string
}

var ErrTaskCredentialInvalid = errors.New("credential domain and username are required")

func credentialDefaultPort(protocol string) int {
	switch protocol {
	case "ssh":
		return 22
	case "winrm":
		return 5985
	default:
		return 0
	}
}

func (in TaskCredentialInput) normalize() (TaskCredentialInput, error) {
	in.Protocol = strings.ToLower(strings.TrimSpace(in.Protocol))
	if in.Protocol == "" {
		in.Protocol = "web"
	}
	switch in.Protocol {
	case "web", "ssh", "winrm":
	default:
		return in, fmt.Errorf("unsupported credential protocol %q (web|ssh|winrm)", in.Protocol)
	}
	in.SecretType = strings.ToLower(strings.TrimSpace(in.SecretType))
	if in.SecretType == "" {
		in.SecretType = "password"
	}
	if in.SecretType != "password" && in.SecretType != "key" {
		return in, fmt.Errorf("unsupported secret_type %q (password|key)", in.SecretType)
	}
	if in.SecretType == "key" && in.Protocol == "web" {
		return in, errors.New("a private key is only valid for ssh/winrm credentials")
	}
	in.Domain = strings.TrimSpace(in.Domain)
	in.Username = strings.TrimSpace(in.Username)
	in.Password = strings.TrimSpace(in.Password)
	in.PrivateKey = strings.TrimSpace(in.PrivateKey)
	in.Note = strings.TrimSpace(in.Note)
	if in.Source != "agent" {
		in.Source = "user"
	}
	if in.Domain == "" || in.Username == "" {
		return in, ErrTaskCredentialInvalid
	}
	if in.Port < 0 || in.Port > 65535 {
		return in, errors.New("port must be between 0 and 65535")
	}
	if in.Port == 0 {
		in.Port = credentialDefaultPort(in.Protocol)
	}
	if in.Source == "agent" {
		if in.SecretType == "key" && in.PrivateKey == "" {
			return in, errors.New("agent-recorded key credentials need the private key that was used")
		}
		if in.SecretType == "password" && in.Password == "" {
			return in, errors.New("agent-recorded credentials need the password that was set")
		}
	}
	return in, nil
}

// Credential returns one credential by id, scoped to the task, decrypted.
func (d *DB) Credential(taskID, id int64) (TaskCredential, error) {
	var c TaskCredential
	err := d.QueryRow(`
		SELECT id, task_id, protocol, domain, port, username, password, secret_type, private_key,
		       source, note, created_at, updated_at
		FROM task_credentials WHERE id = $1 AND task_id = $2`, id, taskID).
		Scan(&c.ID, &c.TaskID, &c.Protocol, &c.Domain, &c.Port, &c.Username, &c.Password,
			&c.SecretType, &c.PrivateKey, &c.Source, &c.Note, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return c, err
	}
	if c.Password, err = openSecret(colCredPass, c.Password); err != nil {
		return c, err
	}
	if c.PrivateKey, err = openSecret(colCredKey, c.PrivateKey); err != nil {
		return c, err
	}
	return c, nil
}

// UpsertTaskCredential inserts or updates the scoped (task, protocol, host, port,
// username) entry: an agent re-registering the same account overwrites the secret
// in place instead of piling up a second row.
func (d *DB) UpsertTaskCredential(taskID int64, in TaskCredentialInput) (TaskCredential, error) {
	in, err := in.normalize()
	if err != nil {
		return TaskCredential{}, err
	}
	pass, err := sealSecret(colCredPass, in.Password)
	if err != nil {
		return TaskCredential{}, err
	}
	key, err := sealSecret(colCredKey, in.PrivateKey)
	if err != nil {
		return TaskCredential{}, err
	}
	var c TaskCredential
	err = d.QueryRow(`
		INSERT INTO task_credentials
		    (task_id, protocol, domain, port, username, password, secret_type, private_key, source, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (task_id, protocol, domain, port, username) DO UPDATE
		SET password    = EXCLUDED.password,
		    secret_type = EXCLUDED.secret_type,
		    private_key = CASE WHEN EXCLUDED.private_key = '' THEN task_credentials.private_key ELSE EXCLUDED.private_key END,
		    source      = EXCLUDED.source,
		    note        = CASE WHEN EXCLUDED.note = '' THEN task_credentials.note ELSE EXCLUDED.note END,
		    updated_at  = now()
		RETURNING id, task_id, protocol, domain, port, username, password, secret_type, private_key,
		          source, note, created_at, updated_at`,
		taskID, in.Protocol, in.Domain, in.Port, in.Username, pass, in.SecretType, key, in.Source, in.Note).
		Scan(&c.ID, &c.TaskID, &c.Protocol, &c.Domain, &c.Port, &c.Username, &c.Password,
			&c.SecretType, &c.PrivateKey, &c.Source, &c.Note, &c.CreatedAt, &c.UpdatedAt)
	c.Password, c.PrivateKey = in.Password, in.PrivateKey
	return c, err
}

// TaskCredentials lists a task's credentials, oldest first (the user-declared
// ones keep their position ahead of agent-discovered ones).
func (d *DB) TaskCredentials(taskID int64) ([]TaskCredential, error) {
	rows, err := d.Query(`
		SELECT id, task_id, protocol, domain, port, username, password, secret_type, private_key,
		       source, note, created_at, updated_at
		FROM task_credentials WHERE task_id = $1 ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskCredential
	for rows.Next() {
		var c TaskCredential
		if err := rows.Scan(&c.ID, &c.TaskID, &c.Protocol, &c.Domain, &c.Port, &c.Username,
			&c.Password, &c.SecretType, &c.PrivateKey, &c.Source, &c.Note, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if c.Password, err = openSecret(colCredPass, c.Password); err != nil {
			return nil, err
		}
		if c.PrivateKey, err = openSecret(colCredKey, c.PrivateKey); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteTaskCredential removes one entry; taskID scopes the delete so a stale
// request cannot cross tasks.
func (d *DB) DeleteTaskCredential(taskID, id int64) error {
	tag, err := d.Exec(`DELETE FROM task_credentials WHERE id = $1 AND task_id = $2`, id, taskID)
	if err != nil {
		return err
	}
	if n, _ := tag.RowsAffected(); n == 0 {
		return errors.New("credential not found")
	}
	return nil
}

// Thin wrappers on ExplorationStore: the ToolSet already holds the task's
// exploration store, so credential tools need no extra wiring.

func (s *ExplorationStore) UpsertTaskCredential(taskID int64, in TaskCredentialInput) (TaskCredential, error) {
	return s.db.UpsertTaskCredential(taskID, in)
}

func (s *ExplorationStore) TaskCredentials(taskID int64) ([]TaskCredential, error) {
	return s.db.TaskCredentials(taskID)
}

func (s *ExplorationStore) Credential(taskID, id int64) (TaskCredential, error) {
	return s.db.Credential(taskID, id)
}
