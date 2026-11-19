"use client";

import * as React from "react";

import { DownloadIcon, Loader2Icon, RefreshCwIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DataTableShell } from "@/components/data-table-shell";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, ApiError } from "@/lib/api";
import type { MachineCapture } from "@/lib/types";

type CaptureRow = MachineCapture & { machine: string };

function fmtBytes(n: number) {
  if (!n) return "—";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

function fmtTime(s: string) {
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleString();
}

function fmtDuration(started: string, stopped?: string) {
  if (!stopped) return "running";
  const a = new Date(started).getTime();
  const b = new Date(stopped).getTime();
  if (Number.isNaN(a) || Number.isNaN(b) || b <= a) return "—";
  const s = Math.round((b - a) / 1000);
  if (s < 60) return `${s}s`;
  return `${Math.floor(s / 60)}m${s % 60}s`;
}

export default function PacketCapturesPage() {
  const [rows, setRows] = React.useState<CaptureRow[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [q, setQ] = React.useState("");

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      setRows((await api.machineCaptureList(200)) as CaptureRow[]);
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  const filtered = React.useMemo(() => {
    const t = q.trim().toLowerCase();
    if (!t) return rows;
    return rows.filter(
      (r) =>
        r.machine.toLowerCase().includes(t) ||
        r.bpf_filter.toLowerCase().includes(t) ||
        String(r.task_id ?? "").includes(t) ||
        r.status.toLowerCase().includes(t),
    );
  }, [rows, q]);

  const running = rows.filter((r) => r.status === "running").length;

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">Packet captures</h1>
          <p className="text-sm text-muted-foreground">
            Machine pcaps created by capture_start/capture_stop (tcpdump on Linux, pktmon on Windows). Evidence pairs:
            pcap + handler transcript = complete RCE proof.
          </p>
        </div>
        <Button variant="outline" onClick={() => void load()} disabled={loading}>
          {loading ? <Loader2Icon className="size-4 animate-spin" /> : <RefreshCwIcon className="size-4" />}
          Refresh
        </Button>
      </div>

      <DataTableShell
        toolbar={
          <div className="flex w-full flex-wrap items-center gap-2">
            <span className="text-sm text-muted-foreground">
              {filtered.length} captures{running > 0 ? ` · ${running} running` : ""}
            </span>
            <Input
              className="ml-auto h-8 w-56"
              placeholder="filter: machine, filter, task…"
              value={q}
              onChange={(e) => setQ(e.target.value)}
            />
          </div>
        }
      >
        <Table>
          <TableHeader className="sticky top-0 z-10 bg-card">
            <TableRow>
              <TableHead>ID</TableHead>
              <TableHead>Machine</TableHead>
              <TableHead>Task</TableHead>
              <TableHead>Interface</TableHead>
              <TableHead className="w-full">Filter</TableHead>
              <TableHead>Status</TableHead>
              <TableHead className="text-right">Bytes</TableHead>
              <TableHead className="text-right">Duration</TableHead>
              <TableHead>Started</TableHead>
              <TableHead className="text-right">Pcap</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading ? (
              <TableRow>
                <TableCell colSpan={10} className="py-10 text-center text-muted-foreground">
                  <Loader2Icon className="mx-auto size-5 animate-spin" />
                </TableCell>
              </TableRow>
            ) : filtered.length === 0 ? (
              <TableRow>
                <TableCell colSpan={10} className="py-10 text-center text-muted-foreground">
                  No captures yet — workers create them with capture_start / capture_stop on registered machines.
                </TableCell>
              </TableRow>
            ) : (
              filtered.map((c) => (
                <TableRow key={c.id}>
                  <TableCell className="font-mono text-xs">#{c.id}</TableCell>
                  <TableCell className="text-xs font-medium">{c.machine}</TableCell>
                  <TableCell className="text-xs">
                    {c.task_id ? (
                      <a className="underline-offset-2 hover:underline" href={`/function/tasks/detail?id=${c.task_id}`}>
                        #{c.task_id}
                      </a>
                    ) : (
                      "—"
                    )}
                  </TableCell>
                  <TableCell className="text-xs">{c.iface || "any"}</TableCell>
                  <TableCell className="max-w-[320px] truncate font-mono text-xs">{c.bpf_filter || "all traffic"}</TableCell>
                  <TableCell>
                    <Badge variant={c.status === "running" ? "default" : c.status === "failed" ? "destructive" : "secondary"}>
                      {c.status}
                    </Badge>
                    {c.note && (
                      <div className="mt-0.5 max-w-[180px] truncate text-[10px] text-muted-foreground" title={c.note}>
                        {c.status === "stopped" ? c.note : c.note}
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="text-right text-xs tabular-nums">{fmtBytes(c.bytes)}</TableCell>
                  <TableCell className="text-right text-xs tabular-nums">
                    {fmtDuration(c.started_at, c.stopped_at)}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">{fmtTime(c.started_at)}</TableCell>
                  <TableCell className="text-right">
                    {c.local_path ? (
                      <Button size="sm" variant="outline" asChild>
                        <a href={`/api/machine-captures/${c.id}/download`} download>
                          <DownloadIcon className="size-3.5" />
                        </a>
                      </Button>
                    ) : (
                      <span className="text-xs text-muted-foreground">—</span>
                    )}
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </DataTableShell>
    </div>
  );
}
