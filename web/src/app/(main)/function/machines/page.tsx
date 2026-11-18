"use client";

import * as React from "react";

import { Loader2Icon, PlayIcon, PlusIcon, RefreshCwIcon, RotateCcwIcon, SquareIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, ApiError } from "@/lib/api";
import type { Machine, MachineEvent, MachineInput, MachineKind, MachineStatus } from "@/lib/types";

const EMPTY: MachineInput = {
  name: "",
  description: "",
  os: "",
  kind: "script",
  host: "",
  ssh_port: 22,
  domain: "",
  start_script: "",
  stop_script: "",
  workdir: "",
  data_dir: "",
  protected: false,
  tags: [],
  config: {},
};

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
}

function fmtTime(s: string) {
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleString();
}

function fmtBytes(n: number) {
  if (!n) return "0 B";
  const u = ["B", "KB", "MB", "GB"];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), u.length - 1);
  return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${u[i]}`;
}

export default function MachinesPage() {
  const [machines, setMachines] = React.useState<Machine[]>([]);
  const [statuses, setStatuses] = React.useState<Record<string, MachineStatus>>({});
  const [events, setEvents] = React.useState<MachineEvent[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [busy, setBusy] = React.useState<string>("");
  const [form, setForm] = React.useState<MachineInput>(EMPTY);
  const [editing, setEditing] = React.useState<Machine | null>(null);
  const [detail, setDetail] = React.useState<MachineEvent | null>(null);
  const [filter, setFilter] = React.useState({ machine: "", action: "", q: "" });

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const list = await api.machines();
      setMachines(list);
      const ev = await api.machineEvents({ ...filter, limit: 50 });
      setEvents(ev);
    } catch (e) {
      toast.error(errText(e));
    } finally {
      setLoading(false);
    }
  }, [filter]);

  React.useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function checkStatus(name: string) {
    try {
      const st = await api.machineStatus(name);
      setStatuses((s) => ({ ...s, [name]: st }));
    } catch (e) {
      toast.error(`${name}: ${errText(e)}`);
    }
  }

  async function checkAll() {
    setBusy("status-all");
    try {
      const pairs = await Promise.all(
        machines.map(async (m) => {
          try {
            return [m.name, await api.machineStatus(m.name)] as const;
          } catch {
            return [m.name, null] as const;
          }
        }),
      );
      const next: Record<string, MachineStatus> = {};
      pairs.forEach(([n, st]) => {
        if (st) next[n] = st;
      });
      setStatuses(next);
    } finally {
      setBusy("");
    }
  }

  async function action(m: Machine, act: "start" | "stop" | "reset") {
    setBusy(`${m.name}:${act}`);
    try {
      const st = await api.machineAction(m.name, act, act === "stop" ? false : undefined);
      setStatuses((s) => ({ ...s, [m.name]: st }));
      toast.success(`${m.name}: ${act} ok`);
      const ev = await api.machineEvents({ ...filter, limit: 50 });
      setEvents(ev);
    } catch (e) {
      toast.error(`${m.name} ${act}: ${errText(e)}`);
    } finally {
      setBusy("");
    }
  }

  function startEdit(m?: Machine) {
    if (m) {
      setEditing(m);
      setForm({
        name: m.name,
        description: m.description,
        os: m.os,
        kind: m.kind,
        host: m.host,
        ssh_port: m.ssh_port,
        domain: m.domain,
        start_script: m.start_script ?? "",
        stop_script: m.stop_script ?? "",
        workdir: m.workdir ?? "",
        data_dir: m.data_dir ?? "",
        protected: m.protected,
        tags: m.tags ?? [],
        config: m.config ?? {},
      });
    } else {
      setEditing(null);
      setForm(EMPTY);
    }
  }

  async function save() {
    setBusy("save");
    try {
      const payload: MachineInput = {
        ...form,
        name: form.name.trim(),
        ssh_port: Number(form.ssh_port) || 22,
        tags: form.tags ?? [],
        config: form.config ?? {},
      };
      if (editing) await api.updateMachine(editing.id, payload);
      else await api.createMachine(payload);
      toast.success(editing ? "machine updated" : "machine registered");
      setEditing(null);
      setForm(EMPTY);
      await load();
    } catch (e) {
      toast.error(errText(e));
    } finally {
      setBusy("");
    }
  }

  async function remove(m: Machine) {
    if (!window.confirm(`Delete machine ${m.name}? This removes only the registration, not the VM/disk.`)) return;
    setBusy(`${m.name}:delete`);
    try {
      await api.deleteMachine(m.id);
      await load();
      toast.success("deleted");
    } catch (e) {
      toast.error(errText(e));
    } finally {
      setBusy("");
    }
  }

  async function reloadEvents() {
    try {
      setEvents(await api.machineEvents({ ...filter, limit: 50 }));
    } catch (e) {
      toast.error(errText(e));
    }
  }

  const kindHint =
    form.kind === "libvirt"
      ? "domain is the libvirt domain name (virsh -c qemu:///system)."
      : form.kind === "script"
        ? "start/stop scripts run inside the container; use absolute host paths."
        : "SSH-only: no VM lifecycle, just exec/transfer over the stored credential.";

  return (
    <div className="flex flex-col gap-4 p-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Machines</h1>
          <p className="text-sm text-muted-foreground">
            Registered VMs and SSH targets. Agents can only name these; hosts, domains and scripts are operator-owned.
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => void checkAll()} disabled={busy === "status-all"}>
            {busy === "status-all" ? <Loader2Icon className="size-4 animate-spin" /> : <RefreshCwIcon className="size-4" />}
            Check status
          </Button>
          <Button onClick={() => startEdit()}>
            <PlusIcon className="size-4" /> Register
          </Button>
        </div>
      </div>

      <div className="grid gap-4 lg:grid-cols-[1.4fr_1fr]">
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">Registered machines</CardTitle>
          </CardHeader>
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Kind</TableHead>
                  <TableHead>Target</TableHead>
                  <TableHead>State</TableHead>
                  <TableHead className="text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {loading ? (
                  <TableRow>
                    <TableCell colSpan={5} className="py-8 text-center text-muted-foreground">
                      <Loader2Icon className="mx-auto size-5 animate-spin" />
                    </TableCell>
                  </TableRow>
                ) : machines.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={5} className="py-8 text-center text-muted-foreground">
                      No machines registered.
                    </TableCell>
                  </TableRow>
                ) : (
                  machines.map((m) => {
                    const st = statuses[m.name];
                    return (
                      <TableRow key={m.id}>
                        <TableCell>
                          <div className="font-medium">{m.name}</div>
                          <div className="text-xs text-muted-foreground">{m.description || m.os || "—"}</div>
                        </TableCell>
                        <TableCell>
                          <Badge variant="outline">{m.kind}</Badge>
                          {m.protected && (
                            <Badge variant="secondary" className="ml-1">
                              protected
                            </Badge>
                          )}
                        </TableCell>
                        <TableCell className="text-xs">
                          <div>{m.host || m.domain || "—"}</div>
                          {m.host && <div className="text-muted-foreground">ssh :{m.ssh_port}</div>}
                        </TableCell>
                        <TableCell>
                          {st ? (
                            <div className="flex flex-col gap-1">
                              <Badge variant={st.running ? "default" : "secondary"}>{st.state || (st.running ? "running" : "stopped")}</Badge>
                              <span className="text-xs text-muted-foreground">
                                ssh {st.ssh_reachable ? "up" : "down"}
                                {st.pid ? ` · pid ${st.pid}` : ""}
                              </span>
                            </div>
                          ) : (
                            <Button variant="ghost" size="sm" onClick={() => void checkStatus(m.name)}>
                              check
                            </Button>
                          )}
                        </TableCell>
                        <TableCell className="text-right">
                          <div className="flex justify-end gap-1">
                            {m.kind !== "ssh" && (
                              <>
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  title="start"
                                  disabled={busy === `${m.name}:start`}
                                  onClick={() => void action(m, "start")}
                                >
                                  <PlayIcon className="size-4" />
                                </Button>
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  title="stop"
                                  disabled={busy === `${m.name}:stop`}
                                  onClick={() => void action(m, "stop")}
                                >
                                  <SquareIcon className="size-4" />
                                </Button>
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  title="reset"
                                  disabled={busy === `${m.name}:reset`}
                                  onClick={() => void action(m, "reset")}
                                >
                                  <RotateCcwIcon className="size-4" />
                                </Button>
                              </>
                            )}
                            <Button variant="ghost" size="sm" onClick={() => startEdit(m)}>
                              edit
                            </Button>
                            <Button variant="ghost" size="icon" title="delete" onClick={() => void remove(m)}>
                              <Trash2Icon className="size-4 text-destructive" />
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    );
                  })
                )}
              </TableBody>
            </Table>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">{editing ? `Edit ${editing.name}` : "Register machine"}</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="grid grid-cols-2 gap-2">
              <div className="space-y-1">
                <Label>Name</Label>
                <Input value={form.name} disabled={!!editing} onChange={(e) => setForm({ ...form, name: e.target.value })} />
              </div>
              <div className="space-y-1">
                <Label>Kind</Label>
                <select
                  className="h-9 w-full rounded-md border bg-transparent px-2 text-sm"
                  value={form.kind}
                  onChange={(e) => setForm({ ...form, kind: e.target.value as MachineKind })}
                >
                  <option value="script">script (raw QEMU)</option>
                  <option value="libvirt">libvirt</option>
                  <option value="ssh">ssh only</option>
                </select>
              </div>
            </div>
            <div className="space-y-1">
              <Label>Description</Label>
              <Input value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} />
            </div>
            <div className="grid grid-cols-2 gap-2">
              <div className="space-y-1">
                <Label>Host</Label>
                <Input value={form.host} placeholder="127.0.0.1 or 192.168.122.50" onChange={(e) => setForm({ ...form, host: e.target.value })} />
              </div>
              <div className="space-y-1">
                <Label>SSH port</Label>
                <Input type="number" value={form.ssh_port} onChange={(e) => setForm({ ...form, ssh_port: Number(e.target.value) })} />
              </div>
            </div>
            {form.kind === "libvirt" && (
              <div className="space-y-1">
                <Label>Libvirt domain</Label>
                <Input value={form.domain} onChange={(e) => setForm({ ...form, domain: e.target.value })} />
              </div>
            )}
            {form.kind === "script" && (
              <>
                <div className="space-y-1">
                  <Label>Start script</Label>
                  <Input value={form.start_script} onChange={(e) => setForm({ ...form, start_script: e.target.value })} />
                </div>
                <div className="space-y-1">
                  <Label>Stop script</Label>
                  <Input value={form.stop_script} onChange={(e) => setForm({ ...form, stop_script: e.target.value })} />
                </div>
                <div className="space-y-1">
                  <Label>Workdir</Label>
                  <Input value={form.workdir} onChange={(e) => setForm({ ...form, workdir: e.target.value })} />
                </div>
              </>
            )}
            <div className="space-y-1">
              <Label>OS</Label>
              <Input value={form.os} placeholder="windows 11 26H2 / ubuntu 24.04" onChange={(e) => setForm({ ...form, os: e.target.value })} />
            </div>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={form.protected}
                onChange={(e) => setForm({ ...form, protected: e.target.checked })}
              />
              protected (refuse start/stop/reset)
            </label>
            <p className="text-xs text-muted-foreground">{kindHint}</p>
            <div className="flex justify-end gap-2">
              {editing && (
                <Button variant="outline" onClick={() => startEdit()}>
                  Cancel
                </Button>
              )}
              <Button onClick={() => void save()} disabled={busy === "save" || !form.name.trim()}>
                {busy === "save" && <Loader2Icon className="size-4 animate-spin" />}
                {editing ? "Save" : "Register"}
              </Button>
            </div>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader className="flex flex-row items-center justify-between pb-2">
          <CardTitle className="text-base">Machine evidence</CardTitle>
          <div className="flex gap-2">
            <Input
              className="h-8 w-40"
              placeholder="machine"
              value={filter.machine}
              onChange={(e) => setFilter({ ...filter, machine: e.target.value })}
            />
            <Input
              className="h-8 w-40"
              placeholder="action"
              value={filter.action}
              onChange={(e) => setFilter({ ...filter, action: e.target.value })}
            />
            <Input
              className="h-8 w-56"
              placeholder="search command/output"
              value={filter.q}
              onChange={(e) => setFilter({ ...filter, q: e.target.value })}
            />
            <Button variant="outline" size="sm" onClick={() => void reloadEvents()}>
              Search
            </Button>
          </div>
        </CardHeader>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>When</TableHead>
                <TableHead>Machine</TableHead>
                <TableHead>Action</TableHead>
                <TableHead>Command</TableHead>
                <TableHead>Exit</TableHead>
                <TableHead>Size</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {events.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={6} className="py-8 text-center text-muted-foreground">
                    No machine evidence yet.
                  </TableCell>
                </TableRow>
              ) : (
                events.map((e) => (
                  <TableRow key={e.id} className="cursor-pointer" onClick={() => void api.machineEvent(e.id).then(setDetail).catch((x) => toast.error(errText(x)))}>
                    <TableCell className="whitespace-nowrap text-xs">{fmtTime(e.created_at)}</TableCell>
                    <TableCell className="text-xs">{e.machine}</TableCell>
                    <TableCell>
                      <Badge variant={e.action === "exec" ? "outline" : "secondary"}>{e.action}</Badge>
                    </TableCell>
                    <TableCell className="max-w-[420px] truncate font-mono text-xs">{e.command || "—"}</TableCell>
                    <TableCell className="text-xs">{e.exit_code ?? "—"}</TableCell>
                    <TableCell className="text-xs">{fmtBytes(e.bytes)}</TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Dialog open={!!detail} onOpenChange={(o) => !o && setDetail(null)}>
        <DialogContent className="max-w-4xl">
          <DialogHeader>
            <DialogTitle>
              {detail?.machine} · {detail?.action} #{detail?.id}
            </DialogTitle>
            <DialogDescription>{detail ? fmtTime(detail.created_at) : ""}</DialogDescription>
          </DialogHeader>
          {detail && (
            <div className="space-y-3">
              <div className="text-xs text-muted-foreground">
                {detail.protocol} · {detail.username || "—"} · exit {detail.exit_code ?? "—"} · {detail.duration_ms} ms ·{" "}
                {fmtBytes(detail.bytes)}
              </div>
              <div>
                <div className="mb-1 text-xs font-medium text-muted-foreground">Command</div>
                <pre className="max-h-40 overflow-auto rounded bg-muted p-2 text-xs">{detail.command}</pre>
              </div>
              <div>
                <div className="mb-1 text-xs font-medium text-muted-foreground">stdout</div>
                <pre className="max-h-72 overflow-auto rounded bg-muted p-2 text-xs">{detail.stdout || "—"}</pre>
              </div>
              {detail.stderr ? (
                <div>
                  <div className="mb-1 text-xs font-medium text-muted-foreground">stderr</div>
                  <pre className="max-h-40 overflow-auto rounded bg-muted p-2 text-xs">{detail.stderr}</pre>
                </div>
              ) : null}
            </div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}