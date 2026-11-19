"use client";

import * as React from "react";
import Link from "next/link";

import { ArrowLeftIcon, Loader2Icon } from "lucide-react";

import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { DataTableShell } from "@/components/data-table-shell";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, ApiError } from "@/lib/api";
import type { ProjectDetail } from "@/lib/types";

export default function ProjectDetailPage() {
  const [key, setKey] = React.useState("");
  const [detail, setDetail] = React.useState<ProjectDetail | null>(null);
  const [loading, setLoading] = React.useState(true);

  React.useEffect(() => {
    const k = new URLSearchParams(window.location.search).get("key") ?? "";
    setKey(k);
    document.title = "Project";
    if (!k) {
      setLoading(false);
      return;
    }
    void (async () => {
      try {
        const d = await api.project(k);
        setDetail(d);
        document.title = d.project.name;
      } catch (e) {
        // eslint-disable-next-line no-console
        console.error(e instanceof ApiError ? e.message : String(e));
      } finally {
        setLoading(false);
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  if (loading) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center text-muted-foreground">
        <Loader2Icon className="mr-2 size-5 animate-spin" /> loading project…
      </div>
    );
  }
  if (!key || !detail) {
    return (
      <div className="p-8 text-center text-muted-foreground">
        Missing or unknown project — go back to the{" "}
        <Link className="underline" href="/function/projects">
          Projects page
        </Link>
        .
      </div>
    );
  }

  const p = detail.project;
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      <div className="flex items-start gap-3">
        <Button variant="ghost" size="icon" aria-label="Back to projects" asChild>
          <Link href="/function/projects">
            <ArrowLeftIcon />
          </Link>
        </Button>
        <div className="min-w-0">
          <h1 className="text-xl font-semibold">{p.name}</h1>
          <div className="mt-0.5 flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
            <code>{p.key}</code>
            {p.binaries.length > 0 && <span className="font-mono text-xs">{p.binaries.join(", ")}</span>}
          </div>
          {p.description && <p className="mt-1 text-sm text-muted-foreground">{p.description}</p>}
        </div>
      </div>

      <div className="grid grid-cols-4 gap-3 text-center">
        <div className="rounded-lg border p-3">
          <div className="text-lg font-semibold tabular-nums">{detail.tasks.length}</div>
          <div className="text-xs text-muted-foreground">tasks</div>
        </div>
        <div className="rounded-lg border p-3">
          <div className="text-lg font-semibold tabular-nums">{detail.findings.length}</div>
          <div className="text-xs text-muted-foreground">findings</div>
        </div>
        <div className="rounded-lg border p-3">
          <div className="text-lg font-semibold tabular-nums">{(detail.assets ?? []).length}</div>
          <div className="text-xs text-muted-foreground">binary assets</div>
        </div>
        <div className="rounded-lg border p-3">
          <div className="text-lg font-semibold tabular-nums">
            {detail.crossmap.available
              ? `${Object.values(detail.crossmap.binaries).filter((b) => b.indexed).length}/${p.binaries.length}`
              : "—"}
          </div>
          <div className="text-xs text-muted-foreground">map coverage</div>
        </div>
      </div>

      {(detail.assets ?? []).length > 0 && (
        <DataTableShell className="h-[38%] min-h-[14rem] shrink-0" toolbar={<span className="text-sm text-muted-foreground">Binary assets — every staged generation</span>}>
          <Table>
            <TableHeader className="sticky top-0 z-10 bg-card">
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead className="text-center">Version</TableHead>
                <TableHead className="text-center">Kind</TableHead>
                <TableHead>OS build</TableHead>
                <TableHead>Path</TableHead>
                <TableHead className="text-right">SHA-256</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(detail.assets ?? []).map((a) => (
                <TableRow key={a.id}>
                  <TableCell className="font-mono text-xs font-medium">{a.name}</TableCell>
                  <TableCell className="text-center font-mono text-xs tabular-nums">{a.version || "—"}</TableCell>
                  <TableCell className="text-center">
                    <StatusBadge domain="binary-kind" value={a.kind} />
                  </TableCell>
                  <TableCell className="text-xs">{a.os_build || "—"}</TableCell>
                  <TableCell className="max-w-[20rem] truncate font-mono text-[11px] text-muted-foreground" title={a.path}>
                    {a.path || "—"}
                  </TableCell>
                  <TableCell className="text-right font-mono text-[11px] text-muted-foreground" title={a.sha256}>
                    {a.sha256 ? `${a.sha256.slice(0, 12)}…` : "—"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </DataTableShell>
      )}

      {detail.findings.length > 0 && (
        <DataTableShell className="h-[34%] min-h-[12rem] shrink-0" toolbar={<span className="text-sm text-muted-foreground">Findings</span>}>
          <Table>
            <TableHeader className="sticky top-0 z-10 bg-card">
              <TableRow>
                <TableHead>Severity</TableHead>
                <TableHead className="w-full">Finding</TableHead>
                <TableHead>Status</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {detail.findings.map((f) => (
                <TableRow key={f.id}>
                  <TableCell>
                    <StatusBadge domain="severity" value={f.severity} dot />
                  </TableCell>
                  <TableCell className="max-w-md">
                    <Link className="text-sm hover:underline" href={`/function/findings/detail?id=${f.id}`}>
                      {f.name}
                    </Link>
                    <div className="truncate text-xs text-muted-foreground">{f.vulnclass}</div>
                  </TableCell>
                  <TableCell>
                    <StatusBadge domain="finding" value={f.status} dot />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </DataTableShell>
      )}

      {detail.tasks.length > 0 && (
        <DataTableShell className="h-[28%] min-h-[10rem] shrink-0" toolbar={<span className="text-sm text-muted-foreground">Tasks</span>}>
          <Table>
            <TableHeader className="sticky top-0 z-10 bg-card">
              <TableRow>
                <TableHead className="w-full">Task</TableHead>
                <TableHead>Status</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {detail.tasks.map((t) => (
                <TableRow key={t.id}>
                  <TableCell>
                    <Link className="text-sm hover:underline" href={`/function/tasks/detail?id=${t.id}`}>
                      {t.name}
                    </Link>
                  </TableCell>
                  <TableCell>
                    <StatusBadge domain="task" value={t.status} dot />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </DataTableShell>
      )}
    </div>
  );
}