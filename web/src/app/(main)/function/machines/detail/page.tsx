"use client";

import * as React from "react";

import { useSearchParams } from "next/navigation";
import {
  ActivityIcon,
  ArrowLeftIcon,
  CameraIcon,
  CheckCircle2Icon,
  Loader2Icon,
  MonitorIcon,
  MousePointerClickIcon,
  RefreshCwIcon,
  ServerIcon,
  XCircleIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { DataTableShell } from "@/components/data-table-shell";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, ApiError, sseUrl } from "@/lib/api";
import type { MachineCapture, MachineEvent, MachineStatus } from "@/lib/types";

const TABS = ["overview", "console", "events", "captures", "lab-state"] as const;
type Tab = (typeof TABS)[number];

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
}

function fmtBytes(n?: number) {
  if (!n) return "—";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

export default function MachineDetailPage() {
  const params = useSearchParams();
  const name = params.get("name") ?? "";
  const [tab, setTab] = React.useState<Tab>("overview");
  const [status, setStatus] = React.useState<MachineStatus | null>(null);
  const [labState, setLabState] = React.useState<Record<string, unknown> | null>(null);
  const [labLoading, setLabLoading] = React.useState(false);
  const [testRes, setTestRes] = React.useState<{ ok: boolean; output?: string; error?: string; latency_ms: number } | null>(null);
  const [testing, setTesting] = React.useState(false);

  const loadStatus = React.useCallback(async () => {
    if (!name) return;
    try {
      setStatus(await api.machineStatus(name));
    } catch (e) {
      toast.error(errText(e));
    }
  }, [name]);

  React.useEffect(() => {
    void loadStatus();
  }, [loadStatus]);

  const loadLabState = React.useCallback(async () => {
    if (!name) return;
    setLabLoading(true);
    try {
      setLabState(await api.machineLabState(name));
    } catch (e) {
      toast.error(errText(e));
    } finally {
      setLabLoading(false);
    }
  }, [name]);

  React.useEffect(() => {
    if (tab === "lab-state" && !labState) void loadLabState();
  }, [tab, labState, loadLabState]);

  async function testConnection() {
    setTesting(true);
    setTestRes(null);
    try {
      setTestRes(await api.machineTestConnection(name));
    } catch (e) {
      toast.error(errText(e));
    } finally {
      setTesting(false);
    }
  }

  if (!name) {
    return <div className="p-8 text-sm text-muted-foreground">missing ?name= parameter</div>;
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="sm" onClick={() => window.history.back()}>
            <ArrowLeftIcon className="size-4" />
          </Button>
          <ServerIcon className="size-5 text-muted-foreground" />
          <h1 className="text-xl font-semibold">{name}</h1>
          {status && (
            <Badge variant={status.running ? "default" : "secondary"} className="gap-1">
              {status.running ? "running" : status.state || "stopped"}
            </Badge>
          )}
          {status?.ssh_reachable && <Badge variant="outline">ssh reachable</Badge>}
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={() => void testConnection()} disabled={testing}>
            {testing ? <Loader2Icon className="size-4 animate-spin" /> : <CheckCircle2Icon className="size-4" />}
            Test connection
          </Button>
          <Button variant="outline" size="sm" onClick={() => void loadStatus()}>
            <RefreshCwIcon className="size-4" /> Refresh
          </Button>
        </div>
      </div>

      {testRes && (
        <Card>
          <CardContent className="flex items-start gap-3 pt-4 text-sm">
            {testRes.ok ? (
              <CheckCircle2Icon className="mt-0.5 size-4 text-emerald-600" />
            ) : (
              <XCircleIcon className="mt-0.5 size-4 text-red-600" />
            )}
            <div>
              <div className="font-medium">{testRes.ok ? "SSH connection OK" : "Connection failed"}</div>
              <div className="font-mono text-xs text-muted-foreground">
                {testRes.output || testRes.error} · {testRes.latency_ms} ms
              </div>
            </div>
          </CardContent>
        </Card>
      )}

      <div className="flex flex-wrap gap-1 border-b pb-2">
        {TABS.map((t) => (
          <button
            key={t}
            type="button"
            onClick={() => setTab(t)}
            className={`cursor-pointer rounded-md px-3 py-1.5 text-sm ${
              tab === t ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:bg-muted"
            }`}
          >
            {t === "lab-state" ? "Lab state" : t.charAt(0).toUpperCase() + t.slice(1)}
          </button>
        ))}
      </div>

      {tab === "overview" && (
        <OverviewTab status={status} labState={labState} onNeedLabState={() => void loadLabState()} />
      )}
      {tab === "console" && <ConsoleTab name={name} />}
      {tab === "events" && <EventsTab name={name} />}
      {tab === "captures" && <CapturesTab name={name} />}
      {tab === "lab-state" && <LabStateTab state={labState} loading={labLoading} onRefresh={() => void loadLabState()} />}
    </div>
  );
}

function OverviewTab({
  status,
  labState,
  onNeedLabState,
}: {
  status: MachineStatus | null;
  labState: Record<string, unknown> | null;
  onNeedLabState: () => void;
}) {
  React.useEffect(() => {
    if (!labState) onNeedLabState();
  }, [labState, onNeedLabState]);
  const rows: [string, string][] = [
    ["State", status?.state ?? "—"],
    ["Running", status?.running ? "yes" : "no"],
    ["Host", status?.host ?? "—"],
    ["SSH", status?.ssh_reachable ? `reachable :${status.ssh_port}` : "unreachable"],
    ["Detail", status?.detail ?? "—"],
  ];
  if (labState) {
    for (const k of ["uptime_s", "free_disk_mb", "free_mem_mb", "office_procs"]) {
      if (labState[k] !== undefined) rows.push([k, String(labState[k])]);
    }
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Overview</CardTitle>
      </CardHeader>
      <CardContent>
        <div className="grid gap-2 sm:grid-cols-2">
          {rows.map(([k, v]) => (
            <div key={k} className="flex justify-between gap-4 border-b pb-1 text-sm">
              <span className="text-muted-foreground">{k}</span>
              <span className="font-mono">{v}</span>
            </div>
          ))}
        </div>
      </CardContent>
    </Card>
  );
}

function ConsoleTab({ name }: { name: string }) {
  const [open, setOpen] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [size, setSize] = React.useState<{ w: number; h: number } | null>(null);
  const [typeText, setTypeText] = React.useState("");
  const [tick, setTick] = React.useState(0); // screenshot refresh

  async function openConsole() {
    setBusy(true);
    try {
      await api.machineConsoleOpen(name);
      setOpen(true);
      setTick((t) => t + 1);
    } catch (e) {
      toast.error(errText(e));
    } finally {
      setBusy(false);
    }
  }
  async function closeConsole() {
    try {
      await api.machineConsoleClose(name);
    } catch (e) {
      toast.error(errText(e));
    }
    setOpen(false);
  }
  async function click(e: React.MouseEvent<HTMLImageElement>) {
    const img = e.currentTarget;
    const r = img.getBoundingClientRect();
    const x = Math.round(((e.clientX - r.left) / r.width) * (size?.w ?? 1));
    const y = Math.round(((e.clientY - r.top) / r.height) * (size?.h ?? 1));
    try {
      await api.machineConsoleInput(name, { action: "click", x, y });
      setTick((t) => t + 1);
    } catch (err) {
      toast.error(errText(err));
    }
  }
  async function sendText() {
    if (!typeText.trim()) return;
    try {
      await api.machineConsoleInput(name, { action: "type", text: typeText });
      setTypeText("");
      setTick((t) => t + 1);
    } catch (err) {
      toast.error(errText(err));
    }
  }

  if (!open) {
    return (
      <Card>
        <CardContent className="flex flex-col items-start gap-3 pt-4">
          <p className="text-sm text-muted-foreground">
            Open a VNC console session to see and drive the machine&apos;s desktop. Screenshots and input go through the
            same channel the agents use.
          </p>
          <Button onClick={() => void openConsole()} disabled={busy}>
            {busy ? <Loader2Icon className="size-4 animate-spin" /> : <MonitorIcon className="size-4" />}
            Open console
          </Button>
        </CardContent>
      </Card>
    );
  }
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0">
        <CardTitle className="text-base">Console</CardTitle>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={() => setTick((t) => t + 1)}>
            <CameraIcon className="size-4" /> Refresh
          </Button>
          <Button variant="outline" size="sm" onClick={() => void closeConsole()}>
            Close
          </Button>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="overflow-auto rounded-md border bg-black">
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img
            key={tick}
            src={sseUrl(`/machines/${encodeURIComponent(name)}/console/screen`)}
            alt="console"
            className="mx-auto max-h-[70vh] cursor-pointer"
            onLoad={(e) => {
              const w = Number(e.currentTarget.naturalWidth);
              const h = Number(e.currentTarget.naturalHeight);
              if (w && h) setSize({ w, h });
            }}
            onClick={(e) => void click(e)}
          />
        </div>
        <div className="flex gap-2">
          <Input
            placeholder="type text into the console…"
            value={typeText}
            onChange={(e) => setTypeText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void sendText();
            }}
          />
          <Button size="sm" onClick={() => void sendText()}>
            <MousePointerClickIcon className="size-4" /> Send
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          Click the screenshot to send a mouse click (coordinates scale automatically). Screenshots are also archived as
          machine events.
        </p>
      </CardContent>
    </Card>
  );
}

function EventsTab({ name }: { name: string }) {
  const [rows, setRows] = React.useState<MachineEvent[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [q, setQ] = React.useState("");

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      setRows(await api.machineEvents({ machine: name, limit: 100, q }));
    } catch (e) {
      toast.error(errText(e));
    } finally {
      setLoading(false);
    }
  }, [name, q]);

  React.useEffect(() => {
    void load();
  }, [load]);

  return (
    <DataTableShell
      toolbar={
        <div className="flex w-full items-center gap-2">
          <span className="text-sm text-muted-foreground">{rows.length} events{loading ? " · loading…" : ""}</span>
          <Input
            className="ml-auto h-8 w-64"
            placeholder="filter: action, task, command…"
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
        </div>
      }
    >
      <Table>
        <TableHeader className="sticky top-0 z-10 bg-card">
          <TableRow>
            <TableHead className="w-16">ID</TableHead>
            <TableHead className="w-24">Task</TableHead>
            <TableHead className="w-28">Action</TableHead>
            <TableHead>Command / detail</TableHead>
            <TableHead className="w-16">Exit</TableHead>
            <TableHead className="w-20">Bytes</TableHead>
            <TableHead className="w-36">Time</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((e) => (
            <TableRow key={e.id}>
              <TableCell className="font-mono text-xs">{e.id}</TableCell>
              <TableCell>{e.task_id ?? "—"}</TableCell>
              <TableCell>
                <Badge variant="outline">{e.action}</Badge>
              </TableCell>
              <TableCell className="max-w-[420px] truncate font-mono text-xs" title={e.command}>
                {e.command || "—"}
              </TableCell>
              <TableCell>{e.exit_code ?? "—"}</TableCell>
              <TableCell>{fmtBytes(e.bytes)}</TableCell>
              <TableCell className="text-xs text-muted-foreground">{new Date(e.created_at).toLocaleString()}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </DataTableShell>
  );
}

function CapturesTab({ name }: { name: string }) {
  const [rows, setRows] = React.useState<(MachineCapture & { machine: string })[]>([]);
  const [loading, setLoading] = React.useState(true);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      setRows((await api.machineCaptures(name)) as (MachineCapture & { machine: string })[]);
    } catch (e) {
      toast.error(errText(e));
    } finally {
      setLoading(false);
    }
  }, [name]);

  React.useEffect(() => {
    void load();
  }, [load]);

  return (
    <DataTableShell
      toolbar={
        <span className="text-sm text-muted-foreground">{rows.length} captures{loading ? " · loading…" : ""}</span>
      }
    >
      <Table>
        <TableHeader className="sticky top-0 z-10 bg-card">
          <TableRow>
            <TableHead className="w-16">ID</TableHead>
            <TableHead>Filter</TableHead>
            <TableHead className="w-24">Status</TableHead>
            <TableHead className="w-24">Size</TableHead>
            <TableHead className="w-24">Packets</TableHead>
            <TableHead className="w-36">Started</TableHead>
            <TableHead className="w-20">Download</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((c) => (
            <TableRow key={c.id}>
              <TableCell className="font-mono text-xs">{c.id}</TableCell>
              <TableCell className="font-mono text-xs">{c.bpf_filter || "—"}</TableCell>
              <TableCell>
                <Badge variant={c.status === "running" ? "default" : "secondary"}>{c.status}</Badge>
              </TableCell>
              <TableCell>{fmtBytes(c.bytes)}</TableCell>
              <TableCell className="text-xs">{c.note?.match(/\d+ packets/)?.[0] ?? "—"}</TableCell>
              <TableCell className="text-xs text-muted-foreground">{new Date(c.started_at).toLocaleString()}</TableCell>
              <TableCell>
                {c.status !== "running" && (
                  <a href={`/api/machine-captures/${c.id}/download`} className="text-sm underline">
                    pcap
                  </a>
                )}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </DataTableShell>
  );
}

function LabStateTab({
  state,
  loading,
  onRefresh,
}: {
  state: Record<string, unknown> | null;
  loading: boolean;
  onRefresh: () => void;
}) {
  if (loading && !state) {
    return (
      <div className="flex items-center gap-2 p-8 text-sm text-muted-foreground">
        <Loader2Icon className="size-4 animate-spin" /> probing the machine…
      </div>
    );
  }
  if (!state) return null;
  const get = (k: string) => (state[k] === undefined ? "—" : String(state[k]));
  const pageHeapWord = get("page_heap_WINWORD");
  const pageHeapExcel = get("page_heap_EXCEL");
  const health: { label: string; ok: boolean | null; value: string }[] = [
    { label: "SSH", ok: state.ssh_ok === true, value: state.ssh_ok ? "ok" : String(state.ssh_error ?? "failed") },
    {
      label: "page_heap WINWORD",
      ok: pageHeapWord === "off" ? true : pageHeapWord === "—" ? null : false,
      value: pageHeapWord,
    },
    {
      label: "page_heap EXCEL",
      ok: pageHeapExcel === "off" ? true : pageHeapExcel === "—" ? null : false,
      value: pageHeapExcel,
    },
    {
      label: "fuzzer processes",
      ok: Number(state.fuzzer_procs ?? 0) > 0 ? true : null,
      value: get("fuzzer_procs"),
    },
    {
      label: "defender exclusions",
      ok: state.defender_exclusions === "unknown" ? null : Number(state.defender_exclusions ?? 0) > 0,
      value: get("defender_exclusions"),
    },
    { label: "windbg bridge", ok: state.windbg_bridge === true, value: state.windbg_bridge ? "reachable" : "down" },
    { label: "office processes", ok: null, value: get("office_procs") },
    { label: "free disk", ok: null, value: `${get("free_disk_mb")} MB` },
    { label: "free memory", ok: null, value: `${get("free_mem_mb")} MB` },
    { label: "uptime", ok: null, value: `${get("uptime_s")} s` },
  ];
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0">
        <CardTitle className="flex items-center gap-2 text-base">
          <ActivityIcon className="size-4" /> Lab state
        </CardTitle>
        <Button variant="outline" size="sm" onClick={onRefresh} disabled={loading}>
          {loading ? <Loader2Icon className="size-4 animate-spin" /> : <RefreshCwIcon className="size-4" />} Re-probe
        </Button>
      </CardHeader>
      <CardContent>
        <div className="grid gap-2 sm:grid-cols-2">
          {health.map((h) => (
            <div key={h.label} className="flex items-center justify-between gap-3 border-b pb-1 text-sm">
              <span className="text-muted-foreground">{h.label}</span>
              <span className="flex items-center gap-1.5 font-mono text-xs">
                {h.ok === true && <CheckCircle2Icon className="size-3.5 text-emerald-600" />}
                {h.ok === false && <XCircleIcon className="size-3.5 text-red-600" />}
                {h.value}
              </span>
            </div>
          ))}
        </div>
        <p className="mt-3 text-xs text-muted-foreground">
          page_heap ON means a triage run left instrumentation enabled — Office repro results are tainted until disabled.
          The windbg bridge is checked from the server host.
        </p>
      </CardContent>
    </Card>
  );
}
