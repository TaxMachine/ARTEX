"use client";

import * as React from "react";

import { BoxesIcon, Loader2Icon, PlusIcon, RefreshCwIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DataTableShell } from "@/components/data-table-shell";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, ApiError } from "@/lib/api";
import type { ProjectDetail, ProjectSummary } from "@/lib/types";
import { cn } from "@/lib/utils";

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
}

function fmtTime(s: string) {
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleDateString();
}

const sevTone: Record<string, string> = {
  critical: "text-red-500",
  high: "text-orange-500",
  medium: "text-amber-500",
  low: "text-blue-400",
};

export default function ProjectsPage() {
  const [projects, setProjects] = React.useState<ProjectSummary[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [detail, setDetail] = React.useState<ProjectDetail | null>(null);
  const [detailKey, setDetailKey] = React.useState("");
  const [detailLoading, setDetailLoading] = React.useState(false);
  const [showCreate, setShowCreate] = React.useState(false);
  const [form, setForm] = React.useState({ name: "", key: "", description: "", binaries: "" });
  const [creating, setCreating] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      setProjects(await api.projects());
    } catch (e) {
      toast.error(errText(e));
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);


  async function create() {
    setCreating(true);
    try {
      await api.createProject({
        name: form.name.trim(),
        key: form.key.trim() || undefined,
        description: form.description.trim(),
        binaries: form.binaries
          .split(",")
          .map((b) => b.trim())
          .filter(Boolean),
      });
      toast.success("project created");
      setShowCreate(false);
      setForm({ name: "", key: "", description: "", binaries: "" });
      await load();
    } catch (e) {
      toast.error(errText(e));
    } finally {
      setCreating(false);
    }
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">Projects</h1>
          <p className="text-sm text-muted-foreground">
            One project per target software. Tasks on the same software link here automatically and cross-reference
            their findings, binaries and function-map coverage.
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => void load()} disabled={loading}>
            {loading ? <Loader2Icon className="size-4 animate-spin" /> : <RefreshCwIcon className="size-4" />}
            Refresh
          </Button>
          <Button onClick={() => setShowCreate(true)}>
            <PlusIcon className="size-4" /> New project
          </Button>
        </div>
      </div>

      <DataTableShell toolbar={<span className="text-sm text-muted-foreground">{projects.length} projects</span>}>
        <Table>
          <TableHeader className="sticky top-0 z-10 bg-card">
            <TableRow>
              <TableHead>Project</TableHead>
              <TableHead>Binaries</TableHead>
              <TableHead>Tasks</TableHead>
              <TableHead>Findings</TableHead>
              <TableHead>Binaries staged</TableHead>
              <TableHead>Updated</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading ? (
              <TableRow>
                <TableCell colSpan={6} className="py-10 text-center text-muted-foreground">
                  <Loader2Icon className="mx-auto size-5 animate-spin" />
                </TableCell>
              </TableRow>
            ) : projects.length === 0 ? (
              <TableRow>
                <TableCell colSpan={6} className="py-10 text-center text-muted-foreground">
                  No projects yet. Create one, or just create a task whose goal names a binary (e.g. clfs.sys) — it
                  links automatically once the project exists.
                </TableCell>
              </TableRow>
            ) : (
              projects.map((p) => (
                <TableRow key={p.id} className="cursor-pointer" >
                  <TableCell>
                    <div className="flex items-center gap-2">
                      <BoxesIcon className="size-4 text-muted-foreground" />
                      <div>
                        <div className="font-medium">{p.name}</div>
                        <code className="text-xs text-muted-foreground">{p.key}</code>
                      </div>
                    </div>
                  </TableCell>
                  <TableCell className="text-xs">
                    {p.binaries.length ? (
                      <div className="flex flex-wrap gap-1">
                        {p.binaries.slice(0, 4).map((b) => (
                          <Badge key={b} variant="outline" className="font-mono text-[10px]">
                            {b}
                          </Badge>
                        ))}
                        {p.binaries.length > 4 && <span className="text-muted-foreground">+{p.binaries.length - 4}</span>}
                      </div>
                    ) : (
                      "—"
                    )}
                  </TableCell>
                  <TableCell className="text-xs tabular-nums">{p.task_count}</TableCell>
                  <TableCell className="text-xs tabular-nums">{p.finding_count}</TableCell>
                  <TableCell className="text-xs tabular-nums">{p.binary_count}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">{fmtTime(p.updated_at)}</TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </DataTableShell>

      <Dialog open={showCreate} onOpenChange={setShowCreate}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>New project</DialogTitle>
            <DialogDescription>
              One per target software. Binaries are the file names tasks stage (clfs.sys, afd.sys) — the auto-link and
              the function map join on them.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-1">
              <Label>Name</Label>
              <Input placeholder="clfs.sys (Windows CLFS driver)" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
            </div>
            <div className="grid grid-cols-2 gap-2">
              <div className="space-y-1">
                <Label>Key (optional)</Label>
                <Input placeholder="clfs-sys" value={form.key} onChange={(e) => setForm({ ...form, key: e.target.value })} />
              </div>
              <div className="space-y-1">
                <Label>Binaries (comma-separated)</Label>
                <Input placeholder="clfs.sys" value={form.binaries} onChange={(e) => setForm({ ...form, binaries: e.target.value })} />
              </div>
            </div>
            <div className="space-y-1">
              <Label>Description</Label>
              <Input placeholder="what this target is" value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} />
            </div>
            <div className="flex justify-end">
              <Button onClick={() => void create()} disabled={creating || !form.name.trim()}>
                {creating && <Loader2Icon className="size-4 animate-spin" />}
                Create
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>

      <Dialog open={!!detailKey} onOpenChange={(o) => !o && setDetailKey("")}>
        <DialogContent className="max-w-3xl max-h-[85vh] overflow-y-auto">
          {detailLoading ? (
            <div className="py-10 text-center text-muted-foreground">
              <Loader2Icon className="mx-auto size-5 animate-spin" />
            </div>
          ) : detail ? (
            <>
              <DialogHeader>
                <DialogTitle className="flex items-center gap-2">
                  {detail.project.name} <code className="text-xs text-muted-foreground">{detail.project.key}</code>
                </DialogTitle>
                <DialogDescription>
                  {detail.project.description || "no description"}
                  {detail.project.binaries.length > 0 && (
                    <span className="ml-2 font-mono text-xs">{detail.project.binaries.join(", ")}</span>
                  )}
                </DialogDescription>
              </DialogHeader>
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
                  <div className="text-lg font-semibold tabular-nums">
                    {detail.crossmap.available
                      ? `${Object.values(detail.crossmap.binaries).filter((b) => b.indexed).length}/${detail.project.binaries.length}`
                      : "—"}
                  </div>
                  <div className="text-xs text-muted-foreground">map coverage</div>
                </div>
                <div className="rounded-lg border p-3">
                  <div className="text-lg font-semibold tabular-nums">{(detail.assets ?? []).length}</div>
                  <div className="text-xs text-muted-foreground">binary assets</div>
                </div>
              </div>
              {(detail.assets ?? []).length > 0 && (
                <div>
                  <h3 className="mb-1 text-xs font-medium text-muted-foreground uppercase tracking-wide">Binary assets</h3>
                  <div className="space-y-1">
                    {(detail.assets ?? []).map((a) => (
                      <div key={a.id} className="rounded-md border px-3 py-1.5">
                        <div className="flex items-center justify-between gap-2">
                          <span className="min-w-0 truncate font-mono text-sm">{a.name}</span>
                          <span className="flex shrink-0 items-center gap-1">
                            {a.version ? <Badge variant="outline" className="font-mono text-[10px]">{a.version}</Badge> : null}
                            <Badge variant={a.kind === "driver" ? "default" : "secondary"} className="text-[10px]">{a.kind}</Badge>
                          </span>
                        </div>
                        <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-[11px] text-muted-foreground">
                          {a.os_build ? <span>{a.os_build}</span> : null}
                          {a.arch ? <span>{a.arch}</span> : null}
                          {a.path ? (
                            <span className="max-w-[22rem] truncate font-mono" title={a.path}>{a.path}</span>
                          ) : null}
                          {a.sha256 ? (
                            <span className="font-mono" title={a.sha256}>{a.sha256.slice(0, 12)}…</span>
                          ) : null}
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
              )}
              {detail.findings.length > 0 && (
                <div>
                  <h3 className="mb-1 text-xs font-medium text-muted-foreground uppercase tracking-wide">Findings</h3>
                  <div className="space-y-1">
                    {detail.findings.map((f) => (
                      <a
                        key={f.id}
                        href={`/function/findings/detail?id=${f.id}`}
                        className="flex items-center justify-between rounded-md border px-3 py-1.5 text-sm hover:bg-accent"
                      >
                        <span className="min-w-0 truncate">{f.name}</span>
                        <span className="ml-2 flex shrink-0 items-center gap-1">
                          <span className={cn("font-mono text-xs font-semibold", sevTone[f.severity])}>{f.severity}</span>
                          <Badge variant="outline" className="text-[10px]">{f.status}</Badge>
                        </span>
                      </a>
                    ))}
                  </div>
                </div>
              )}
              {detail.tasks.length > 0 && (
                <div>
                  <h3 className="mb-1 text-xs font-medium text-muted-foreground uppercase tracking-wide">Tasks</h3>
                  <div className="space-y-1">
                    {detail.tasks.map((t) => (
                      <a
                        key={t.id}
                        href={`/function/tasks/detail?id=${t.id}`}
                        className="flex items-center justify-between rounded-md border px-3 py-1.5 text-sm hover:bg-accent"
                      >
                        <span className="min-w-0 truncate">{t.name}</span>
                        <Badge variant="outline" className="ml-2 shrink-0 text-xs">
                          {t.status}
                        </Badge>
                      </a>
                    ))}
                  </div>
                </div>
              )}
            </>
          ) : (
            <div className="py-8 text-center text-muted-foreground">failed to load project</div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}