package agent

import (
	"context"
	"encoding/json"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// Task credential tools (#3): a per-task record of test accounts. User-declared
// accounts (e.g. a program-provided alias) are injected into the worker's graph
// overview; accounts the agent registers itself MUST be recorded via
// save_task_credential so they are not forgotten when the session context rolls
// over. Handlers guard nil stores internally, mirroring the asset tools.

func (t *ToolSet) saveTaskCredential() actool.CoreTool {
	return writeTool(
		"save_task_credential",
		"Record (or update) a test account for this task's target. ALWAYS call this immediately after registering an account, or after obtaining machine access, with the exact secret you used, so it is not lost. Also use it to update a known account whose secret changed. Same (protocol, domain, port, username) is overwritten in place, not duplicated.\n"+
			"For web logins use the defaults (protocol=web, password). For a lab machine use protocol=ssh, domain=the machine host (or name), port (default 22), and either password or secret_type=key with private_key.\n"+
			"Returns: {id, protocol, domain, port, username, secret_type, source}.",
		obj(map[string]any{
			"protocol":    map[string]any{"type": "string", "enum": []string{"web", "ssh", "winrm"}, "description": "credential kind (default web)"},
			"domain":      str("domain or host the credential belongs to (e.g. target.example.com or 192.168.122.50)"),
			"port":        intp("service port (0 = protocol default: ssh 22, winrm 5985)"),
			"username":    str("login name / email / SSH user"),
			"password":    str("the exact password that was set (password credentials)"),
			"secret_type": map[string]any{"type": "string", "enum": []string{"password", "key"}, "description": "password (default) or key"},
			"private_key": str("PEM private key for secret_type=key (unencrypted keys only)"),
			"note":        str("optional context, e.g. which registration flow created it"),
		}, "domain", "username"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Protocol   string `json:"protocol"`
				Domain     string `json:"domain"`
				Port       int    `json:"port"`
				Username   string `json:"username"`
				Password   string `json:"password"`
				SecretType string `json:"secret_type"`
				PrivateKey string `json:"private_key"`
				Note       string `json:"note"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("invalid input: " + err.Error()), nil
			}
			if t.ts == nil {
				return actool.Errorf("save_task_credential is not enabled: task store is not initialized"), nil
			}
			taskID := t.taskID
			if taskID <= 0 {
				var err error
				taskID, err = t.ts.TaskID()
				if err != nil || taskID <= 0 {
					return actool.Errorf("save_task_credential is only available inside a task"), nil
				}
				t.taskID = taskID
			}
			c, err := t.ts.UpsertTaskCredential(taskID, db.TaskCredentialInput{
				Protocol: a.Protocol, Domain: a.Domain, Port: a.Port,
				Username: a.Username, Password: a.Password,
				SecretType: a.SecretType, PrivateKey: a.PrivateKey,
				Source: "agent", Note: a.Note,
			})
			if err != nil {
				return actool.Errorf("save_task_credential failed: " + err.Error()), nil
			}
			return jsonResult(map[string]any{
				"id": c.ID, "protocol": c.Protocol, "domain": c.Domain, "port": c.Port,
				"username": c.Username, "secret_type": c.SecretType, "source": c.Source,
			})
		})
}

func (t *ToolSet) listTaskCredentials() actool.CoreTool {
	return writeTool(
		"list_task_credentials",
		"List this task's test accounts: user-declared accounts (e.g. program-provided aliases; a user-declared entry with an empty password means: register an account on that domain with a strong password, then record it via save_task_credential) plus accounts recorded by earlier runs. Check this BEFORE registering a new account, and use matching entries for authenticated testing.",
		obj(map[string]any{}),
		func(_ context.Context, _ json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("list_task_credentials is not enabled: task store is not initialized"), nil
			}
			taskID := t.taskID
			if taskID <= 0 {
				var err error
				taskID, err = t.ts.TaskID()
				if err != nil || taskID <= 0 {
					return actool.Errorf("list_task_credentials is only available inside a task"), nil
				}
				t.taskID = taskID
			}
			list, err := t.ts.TaskCredentials(taskID)
			if err != nil {
				return actool.Errorf("list_task_credentials failed: " + err.Error()), nil
			}
			// Never put a private key into model context: the machine tools resolve
			// it server-side. Passwords stay (they are the existing web-auth flow).
			for i := range list {
				if list[i].PrivateKey != "" {
					list[i].PrivateKey = "stored (use credential_id with ssh_exec/ssh_upload/ssh_download)"
				}
			}
			return jsonResult(map[string]any{"credentials": list})
		})
}

// taskCredentialOverview renders the credentials block injected into the worker's
// graph overview. Empty user-side passwords surface as an explicit instruction
// (the agent generates and records one) rather than a half-entry.
func (t *ToolSet) taskCredentialOverview() []map[string]any {
	if t.ts == nil || t.taskID <= 0 {
		return nil
	}
	list, err := t.ts.TaskCredentials(t.taskID)
	if err != nil || len(list) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, c := range list {
		m := map[string]any{
			"protocol": c.Protocol,
			"domain":   c.Domain,
			"port":     c.Port,
			"username": c.Username,
			"source":   c.Source,
		}
		if c.Note != "" {
			m["note"] = c.Note
		}
		switch {
		case c.SecretType == "key":
			m["secret_type"] = "key"
			m["instruction"] = "SSH key is stored; the machine tools use it automatically"
		case c.Password == "":
			m["instruction"] = "no password yet: register an account with this username on this domain (choose a strong unique password), then record it via save_task_credential"
		default:
			m["password"] = c.Password
		}
		out = append(out, m)
	}
	return out
}
