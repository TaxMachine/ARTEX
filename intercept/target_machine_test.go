package intercept

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// Machine tools declare their target through the machine registry: the input
// names a machine, extraction resolves it to the registered endpoint, and the
// pin lookup keys on host:ssh_port. Unresolvable machines and non-network
// machine tools declare nothing.
func TestMachineToolTargetExtraction(t *testing.T) {
	old := machineEndpointResolver
	t.Cleanup(func() { machineEndpointResolver = old })
	SetMachineEndpointResolver(func(machine string) []string {
		if machine == "v424" {
			return []string{"127.0.0.1:10030"}
		}
		return nil
	})

	cases := []struct {
		tool  string
		input string
		want  []Target
	}{
		{
			tool:  "ssh_exec",
			input: `{"machine":"v424","command":"whoami"}`,
			want:  []Target{{Kind: TargetIP, Host: "127.0.0.1", Port: 10030}},
		},
		{
			tool:  "ssh_upload",
			input: `{"machine":"v424","local_path":"/tmp/x","remote_path":"C:/x"}`,
			want:  []Target{{Kind: TargetIP, Host: "127.0.0.1", Port: 10030}},
		},
		{
			tool:  "ssh_download",
			input: `{"machine":"v424","remote_path":"C:/x"}`,
			want:  []Target{{Kind: TargetIP, Host: "127.0.0.1", Port: 10030}},
		},
		{
			tool:  "capture_start",
			input: `{"machine":"v424","iface":"eth0","bpf_filter":"tcp"}`,
			want:  []Target{{Kind: TargetIP, Host: "127.0.0.1", Port: 10030}},
		},
		{
			tool:  "capture_stop",
			input: `{"machine":"v424","capture_id":7}`,
			want:  []Target{{Kind: TargetIP, Host: "127.0.0.1", Port: 10030}},
		},
		{
			// A command string naming hosts must not be scanned on machine tools:
			// the machine field decides the target, not the command text.
			tool:  "ssh_exec",
			input: `{"machine":"v424","command":"curl http://203.0.113.9:8080/"}`,
			want:  []Target{{Kind: TargetIP, Host: "127.0.0.1", Port: 10030}},
		},
		{
			// Unknown machine: nothing declared; the tool errors on its own.
			tool:  "ssh_exec",
			input: `{"machine":"nope","command":"whoami"}`,
			want:  nil,
		},
		{
			// Read-only machine tools never declare a target.
			tool:  "machine_status",
			input: `{"machine":"v424"}`,
			want:  nil,
		},
		{
			tool:  "machine_events",
			input: `{"machine":"v424"}`,
			want:  nil,
		},
	}
	for _, c := range cases {
		got := ExtractTargets(c.tool, []byte(c.input))
		if len(got) != len(c.want) {
			t.Fatalf("%s %s: targets = %+v, want %+v", c.tool, c.input, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s %s: target[%d] = %+v, want %+v", c.tool, c.input, i, got[i], c.want[i])
			}
		}
	}
}

// Without a resolver installed, machine tools declare no target (the gate stays
// off for them, matching the pre-pinning behavior).
func TestMachineToolExtractionWithoutResolver(t *testing.T) {
	old := machineEndpointResolver
	t.Cleanup(func() { machineEndpointResolver = old })
	SetMachineEndpointResolver(nil)
	if got := ExtractTargets("ssh_exec", []byte(`{"machine":"v424","command":"x"}`)); len(got) != 0 {
		t.Fatalf("targets = %+v, want none without a resolver", got)
	}
}

// The machine endpoint flows through the gate like any declared target: an
// unpinned ssh_exec call reports exactly the registered endpoint, and a pin on
// that endpoint covers later calls of the same machine.
func TestMachineEndpointGateFlow(t *testing.T) {
	d := targetTestDB(t)
	ic, _ := pgGate(t, d)
	old := machineEndpointResolver
	t.Cleanup(func() { machineEndpointResolver = old })
	SetMachineEndpointResolver(func(machine string) []string {
		return []string{"127.0.0.1:10030"}
	})

	// A short wait bound keeps the pending-verification step quick; the waiter
	// returns TARGET_APPROVAL_PENDING when the bound elapses with no decision.
	scope := taskScope(t, d, ic, 2*time.Second)
	input, _ := json.Marshal(map[string]any{"machine": "v424", "command": "whoami"})
	unpinned, res := ic.CheckTargets(context.Background(), scope, "ssh_exec", input)
	if res.Code != "" {
		t.Fatalf("check: %+v", res)
	}
	if len(unpinned) != 1 || unpinned[0].Host != "127.0.0.1" || unpinned[0].Port != 10030 {
		t.Fatalf("unpinned = %+v, want the machine endpoint 127.0.0.1:10030", unpinned)
	}

	// The request row the waiter creates is keyed by the endpoint.
	reqs := []TargetRequest{{
		Host: unpinned[0].Host, Port: unpinned[0].Port, Kind: TargetIP,
		ResolvedIPs: []string{"127.0.0.1"}, Resolver: "registry", ResolvedAt: time.Now().UTC(), Reason: "no_pin",
	}}
	out := request(context.Background(), ic, scope, nil, reqs...)
	blocked := result(t, out)
	if blocked.Code != TargetApprovalPending {
		t.Fatalf("wait result = %+v, want TARGET_APPROVAL_PENDING", blocked)
	}
	ids := openRows(t, d, scope)
	if len(ids) != 1 {
		t.Fatalf("open requests = %v, want one", ids)
	}
	key, err := d.TargetRequestKey(ids[0])
	if err != nil || key != "127.0.0.1:10030" {
		t.Fatalf("request key = %q (%v), want 127.0.0.1:10030", key, err)
	}

	// Approve: the pin covers the endpoint and the next call passes.
	if _, ok, err := d.ResolveTargetRequest(ids[0], true, "test", ""); err != nil || !ok {
		t.Fatalf("approve: ok=%v err=%v", ok, err)
	}
	if again, res := ic.CheckTargets(context.Background(), scope, "ssh_exec", input); res.Code != "" || len(again) != 0 {
		t.Fatalf("after approval: unpinned=%+v res=%+v, want covered", again, res)
	}
	// And the upload to the same machine passes on the same pin.
	up, _ := json.Marshal(map[string]any{"machine": "v424", "local_path": "/tmp/x", "remote_path": "C:/x"})
	if got, res := ic.CheckTargets(context.Background(), scope, "ssh_upload", up); res.Code != "" || len(got) != 0 {
		t.Fatalf("upload after approval: unpinned=%+v res=%+v, want covered", got, res)
	}
}
