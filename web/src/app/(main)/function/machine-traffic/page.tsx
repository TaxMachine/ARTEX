"use client";

import * as React from "react";

import { Loader2Icon, RefreshCwIcon } from "lucide-react";
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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { DataTableShell } from "@/components/data-table-shell";
import { api, ApiError } from "@/lib/api";
import type { MachineEvent } from "@/lib/types";

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

// Machine traffic is deliberately separate from the web Traffic page: HTTP
// exchanges come from the recording proxy, while these rows are the native
// machine tools' evidence (SSH exec/transfer, VM lifecycle, pcap capture).
export default function MachineTrafficPage() {
  const [events, setEvents] = React.useState<MachineEvent[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [detail, setDetail] = React.useState<MachineEvent | null>(null);
  const [filter, setFilter] = React.useState({ machine: "", action: "", q: "" });

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      setEvents(await api.machineEvents({ ...filter, limit: 100 }));
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

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">Machine Traffic</h1>
          <p className="text-sm text-muted-foreground">
            Native VM/SSH evidence: commands and output, uploads/downloads, lifecycle actions. Packet captures have their own page. Web
            HTTP exchanges stay on the separate Traffic page.
          </p>
        </div>
        <Button variant="outline" onClick={() => void load()} disabled={loading}>
          {loading ? <Loader2Icon className="size-4 animate-spin" /> : <RefreshCwIcon className="size-4" />}
          Refresh
        </Button>
      </div>

      <DataTableShell
        toolbar={
          <>
            <CardTitle className="text-base">Recorded activity</CardTitle>
            <Input
              className="h-8 w-40"
              placeholder="machine"
              value={filter.machine}
              onChange={(e) => setFilter({ ...filter, machine: e.target.value })}
            />
            <Input
              className="h-8 w-40"
              placeholder="action (exec, start, …)"
              value={filter.action}
              onChange={(e) => setFilter({ ...filter, action: e.target.value })}
            />
            <Input
              className="h-8 w-64"
              placeholder="search command/stdout/stderr"
              value={filter.q}
              onChange={(e) => setFilter({ ...filter, q: e.target.value })}
            />
            <Button size="sm" variant="outline" onClick={() => void load()}>
              Search
            </Button>
          </>
        }
      >
        <Table>
          <TableHeader className="sticky top-0 z-10 bg-card">
            <TableRow>
              <TableHead>When</TableHead>
              <TableHead>Machine</TableHead>
              <TableHead>Kind</TableHead>
              <TableHead>Action</TableHead>
              <TableHead>User</TableHead>
              <TableHead>Command / detail</TableHead>
              <TableHead>Exit</TableHead>
              <TableHead>Size</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading ? (
              <TableRow>
                <TableCell colSpan={8} className="py-10 text-center text-muted-foreground">
                  <Loader2Icon className="mx-auto size-5 animate-spin" />
                </TableCell>
              </TableRow>
            ) : events.length === 0 ? (
              <TableRow>
                <TableCell colSpan={8} className="py-10 text-center text-muted-foreground">
                  No machine activity recorded yet. Run an ssh_exec or start a VM.
                </TableCell>
              </TableRow>
            ) : (
              events.map((e) => (
                <TableRow
                  key={e.id}
                  className="cursor-pointer"
                  onClick={() => void api.machineEvent(e.id).then(setDetail).catch((x) => toast.error(errText(x)))}
                >
                  <TableCell className="whitespace-nowrap text-xs">{fmtTime(e.created_at)}</TableCell>
                  <TableCell className="text-xs">{e.machine}</TableCell>
                  <TableCell>
                    <Badge variant="outline">{e.protocol}</Badge>
                  </TableCell>
                  <TableCell>
                    <Badge variant={e.action === "exec" ? "outline" : "secondary"}>{e.action}</Badge>
                  </TableCell>
                  <TableCell className="text-xs">{e.username || "—"}</TableCell>
                  <TableCell className="max-w-[420px] truncate font-mono text-xs">{e.command || "—"}</TableCell>
                  <TableCell className="text-xs">{e.exit_code ?? "—"}</TableCell>
                  <TableCell className="text-xs">{fmtBytes(e.bytes)}</TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </DataTableShell>

      <Card className="shrink-0 py-4">
        <CardContent className="flex flex-wrap items-center justify-between gap-2 px-4">
          <CardTitle className="text-base">Packet captures moved</CardTitle>
          <span className="text-xs text-muted-foreground">
            Machine pcaps now live on their own page — filterable, with task links and pcap downloads.
          </span>
          <Button size="sm" variant="outline" asChild>
            <a href="/function/machine-captures">Open Packet captures</a>
          </Button>
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
                {detail.meta?.host ? ` · ${String(detail.meta.host)}:${String(detail.meta.port ?? "")}` : ""}
              </div>
              <div>
                <div className="mb-1 text-xs font-medium text-muted-foreground">Command</div>
                <pre className="max-h-40 overflow-auto rounded bg-muted p-2 text-xs">{detail.command || "—"}</pre>
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