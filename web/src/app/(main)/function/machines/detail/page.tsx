"use client";

import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import {
  ActivityIcon,
  ArrowLeftIcon,
  CameraIcon,
  CheckCircle2Icon,
  KeyRoundIcon,
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
import { api, ApiError, sseUrl } from "@/lib/api";
import type { MachineStatus } from "@/lib/types";

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
}

function secs(s: unknown) {
  const n = Number(s);
  if (!Number.isFinite(n)) return "—";
  const d = Math.floor(n / 86400);
  const h = Math.floor((n % 86400) / 3600);
  const m = Math.floor((n % 3600) / 60);
  return d > 0 ? `${d}d ${h}h` : h > 0 ? `${h}h ${m}m` : `${m}m`;
}
function mb(s: unknown) {
  const n = Number(s);
  return Number.isFinite(n) ? `${(n / 1024).toFixed(1)} GB` : "—";
}

function Row({ label, value, ok }: { label: string; value: string; ok?: boolean | null }) {
  return (
    <div className="flex items-center justify-between gap-3 border-b py-1.5 text-sm last:border-b-0">
      <span className="text-muted-foreground">{label}</span>
      <span className="flex items-center gap-1.5 font-mono text-xs">
        {ok === true && <CheckCircle2Icon className="size-3.5 text-emerald-600" />}
        {ok === false && <XCircleIcon className="size-3.5 text-red-600" />}
        {value}
      </span>
    </div>
  );
}

export default function MachineDetailPage() {
  const params = useSearchParams();
  const name = params.get("name") ?? "";
  const [status, setStatus] = React.useState<MachineStatus | null>(null);
  const [lab, setLab] = React.useState<Record<string, unknown> | null>(null);
  const [occ, setOcc] = React.useState<Awaited<ReturnType<typeof api.machineOccupancy>> | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [testRes, setTestRes] = React.useState<{ ok: boolean; output?: string; error?: string; latency_ms: number } | null>(null);

  const loadAll = React.useCallback(async () => {
    if (!name) return;
    setLoading(true);
    const [st, lb, oc] = await Promise.allSettled([api.machineStatus(name), api.machineLabState(name), api.machineOccupancy(name)]);
    if (st.status === "fulfilled") setStatus(st.value);
    if (lb.status === "fulfilled") setLab(lb.value);
    if (oc.status === "fulfilled") setOcc(oc.value);
    setLoading(false);
  }, [name]);

  React.useEffect(() => {
    void loadAll();
  }, [loadAll]);

  async function testConn() {
    try {
      setTestRes(await api.machineTestConnection(name));
    } catch (e) {
      toast.error(errText(e));
    }
  }

  async function rdpToggle(enabled: boolean) {
    try {
      const r = await api.machineRdpToggle(name, enabled);
      if (!r.ok) toast.error(`RDP toggle failed: ${r.error ?? r.output}`);
      else toast.success(`Remote Desktop ${enabled ? "enabled" : "disabled"}`);
      void loadAll();
    } catch (e) {
      toast.error(errText(e));
    }
  }

  if (!name) return <div className="p-8 text-sm text-muted-foreground">missing ?name= parameter</div>;

  const rdpOn = String(lab?.rdp_enabled) === "True";
  const pageHeapBad = (k: string) => {
    const v = lab?.[k];
    return v !== undefined && v !== "off" && String(v) !== "";
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="sm" asChild>
            <Link href="/function/machines">
              <ArrowLeftIcon className="size-4" />
            </Link>
          </Button>
          <ServerIcon className="size-5 text-muted-foreground" />
          <h1 className="text-xl font-semibold">{name}</h1>
          {status && (
            <Badge variant={status.running ? "default" : "secondary"}>{status.running ? "up" : status.state || "down"}</Badge>
          )}
          {status?.ssh_reachable && <Badge variant="outline">ssh reachable</Badge>}
          {rdpOn && <Badge className="bg-emerald-600/15 text-emerald-700 dark:text-emerald-400">RDP on</Badge>}
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={() => void testConn()}>
            <CheckCircle2Icon className="size-4" /> Test SSH
          </Button>
          <Button variant="outline" size="sm" onClick={() => void loadAll()} disabled={loading}>
            {loading ? <Loader2Icon className="size-4 animate-spin" /> : <RefreshCwIcon className="size-4" />} Refresh
          </Button>
        </div>
      </div>

      {testRes && (
        <Card>
          <CardContent className="flex items-center gap-3 pt-4 text-sm">
            {testRes.ok ? <CheckCircle2Icon className="size-4 text-emerald-600" /> : <XCircleIcon className="size-4 text-red-600" />}
            <span className="font-mono text-xs">{testRes.ok ? testRes.output : testRes.error} · {testRes.latency_ms} ms</span>
          </CardContent>
        </Card>
      )}

      <div className="grid gap-4 lg:grid-cols-3">
        {/* left: identity + access */}
        <div className="space-y-4">
          <Card>
            <CardHeader className="pb-2">
              <CardTitle className="text-base">Machine</CardTitle>
            </CardHeader>
            <CardContent>
              <Row label="State" value={status?.state ?? "—"} ok={status?.running} />
              <Row label="Host" value={status?.host ?? "—"} />
              <Row label="SSH" value={status?.ssh_reachable ? `reachable :${status.ssh_port}` : "unreachable"} ok={status?.ssh_reachable} />
              <Row label="OS" value={String(lab?.os ?? "—")} />
              <Row label="Uptime" value={secs(lab?.uptime_s)} />
              <Row label="Free disk" value={mb(lab?.free_disk_mb)} ok={Number(lab?.free_disk_mb) > 2048} />
              <Row label="Free memory" value={mb(lab?.free_mem_mb)} ok={Number(lab?.free_mem_mb) > 1024} />
            </CardContent>
          </Card>

          <Card>
            <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
              <CardTitle className="flex items-center gap-2 text-base">
                <KeyRoundIcon className="size-4" /> Remote Desktop
              </CardTitle>
              <Button variant="outline" size="sm" onClick={() => void rdpToggle(!rdpOn)} disabled={!status?.ssh_reachable}>
                {rdpOn ? "Disable" : "Enable"}
              </Button>
            </CardHeader>
            <CardContent>
              <Row label="RDP enabled" value={rdpOn ? "yes" : lab ? "no" : "—"} ok={rdpOn ? true : null} />
              <Row label="TermService" value={String(lab?.rdp_service ?? "—")} />
              <Row label="Port 3389" value={String(lab?.rdp_listening ?? "—") === "1" ? "listening" : lab ? "closed" : "—"} ok={String(lab?.rdp_listening) === "1" ? true : null} />
              <p className="mt-2 text-xs text-muted-foreground">
                Enabling opens 3389 + the firewall group on the machine. The console below uses RDP automatically when it&apos;s on.
              </p>
            </CardContent>
          </Card>
        </div>

        {/* middle: lab state */}
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="flex items-center gap-2 text-base">
              <ActivityIcon className="size-4" /> Lab state
            </CardTitle>
          </CardHeader>
          <CardContent>
            <Row label="page_heap WINWORD" value={String(lab?.page_heap_WINWORD ?? "—")} ok={pageHeapBad("page_heap_WINWORD") ? false : true} />
            <Row label="page_heap EXCEL" value={String(lab?.page_heap_EXCEL ?? "—")} ok={pageHeapBad("page_heap_EXCEL") ? false : true} />
            <Row label="fuzzer processes" value={String(lab?.fuzzer_procs ?? "—")} ok={Number(lab?.fuzzer_procs) > 0 ? true : null} />
            <Row label="office processes" value={String(lab?.office_procs ?? "—")} />
            <Row label="defender exclusions" value={String(lab?.defender_exclusions ?? "—")} ok={Number(lab?.defender_exclusions) > 0} />
            <Row label="windbg bridge" value={lab?.windbg_bridge ? "reachable" : lab ? "down" : "—"} ok={lab?.windbg_bridge === true} />
            {pageHeapBad("page_heap_WINWORD") && (
              <p className="mt-2 text-xs text-red-600">page_heap is ON — repro results are tainted until it&apos;s disabled.</p>
            )}
          </CardContent>
        </Card>

        {/* right: usage + credentials */}
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">Usage &amp; credentials</CardTitle>
          </CardHeader>
          <CardContent>
            <Row label="credential count" value={String(occ?.credentials.count ?? "—")} />
            <Row label="default user" value={occ?.credentials.default_username || "—"} />
            <Row label="password stored" value={occ?.credentials.has_password ? "yes (RDP-capable)" : "no (RDP needs one)"} ok={occ?.credentials.has_password ?? null} />
            <div className="mt-3">
              <div className="mb-1 text-sm font-medium">Recent tasks</div>
              {(occ?.tasks ?? []).length === 0 ? (
                <p className="text-xs text-muted-foreground">none yet</p>
              ) : (
                <div className="space-y-1">
                  {occ!.tasks!.map((t) => (
                    <Link key={t.task_id} href={`/function/tasks/${t.task_id}`} className="flex items-center justify-between text-xs hover:underline">
                      <span className="truncate">
                        <span className="font-mono text-muted-foreground">#{t.task_id}</span> {t.name || "(unnamed)"}
                      </span>
                      <Badge variant="outline" className="ml-2 shrink-0">
                        {t.status}
                      </Badge>
                    </Link>
                  ))}
                </div>
              )}
            </div>
          </CardContent>
        </Card>
      </div>

      {/* bottom: console */}
      <ConsoleCard name={name} rdpOn={rdpOn} hasPassword={occ?.credentials.has_password ?? false} />
    </div>
  );
}

function ConsoleCard({ name, rdpOn, hasPassword }: { name: string; rdpOn: boolean; hasPassword: boolean }) {
  const [open, setOpen] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [size, setSize] = React.useState<{ w: number; h: number } | null>(null);
  const [text, setText] = React.useState("");
  const [tick, setTick] = React.useState(0);

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
  async function send() {
    if (!text.trim()) return;
    try {
      await api.machineConsoleInput(name, { action: "type", text });
      setText("");
      setTick((t) => t + 1);
    } catch (err) {
      toast.error(errText(err));
    }
  }

  const unavailable = !rdpOn || !hasPassword;
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="flex items-center gap-2 text-base">
          <MonitorIcon className="size-4" /> Console {open && <Badge variant="secondary">{rdpOn ? "RDP" : "VNC"}</Badge>}
        </CardTitle>
        {open && (
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => setTick((t) => t + 1)}>
              <CameraIcon className="size-4" /> Refresh
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                void api.machineConsoleClose(name);
                setOpen(false);
              }}
            >
              Close
            </Button>
          </div>
        )}
      </CardHeader>
      <CardContent>
        {!open ? (
          <div className="flex flex-col items-start gap-2">
            <p className="text-sm text-muted-foreground">
              {unavailable
                ? "Console needs Remote Desktop enabled and a machine-level password credential."
                : "Open an interactive session. Click the screenshot to click, type below to send text."}
            </p>
            <Button onClick={() => void openConsole()} disabled={busy || unavailable}>
              {busy ? <Loader2Icon className="size-4 animate-spin" /> : <MonitorIcon className="size-4" />} Open console
            </Button>
          </div>
        ) : (
          <div className="space-y-3">
            <div className="overflow-auto rounded-md border bg-black">
              {/* eslint-disable-next-line @next/next/no-img-element */}
              <img
                key={tick}
                src={sseUrl(`/machines/${encodeURIComponent(name)}/console/screen`)}
                alt="console"
                className="mx-auto max-h-[70vh] cursor-pointer"
                onLoad={(e) => {
                  const w = e.currentTarget.naturalWidth;
                  const h = e.currentTarget.naturalHeight;
                  if (w && h) setSize({ w, h });
                }}
                onClick={(e) => void click(e)}
              />
            </div>
            <div className="flex gap-2">
              <Input
                placeholder="type into the console…"
                value={text}
                onChange={(e) => setText(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") void send();
                }}
              />
              <Button size="sm" onClick={() => void send()}>
                <MousePointerClickIcon className="size-4" /> Send
              </Button>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
