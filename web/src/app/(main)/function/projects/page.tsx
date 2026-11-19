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
import type { ProjectSummary } from "@/lib/types";
import { cn } from "@/lib/utils";

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
}

function fmtTime(s: string) {
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleDateString();
}

const sevTone: Record<string, string> = {
  critical: "text-rose-600 dark:text-rose-400",
  high: "text-red-600 dark:text-red-400",
  medium: "text-amber-600 dark:text-amber-400",
  low: "text-slate-600 dark:text-slate-400",
};

export default function ProjectsPage() {
  const [projects, setProjects] = React.useState<ProjectSummary[]>([]);
  const [loading, setLoading] = React.useState(true);
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
              <TableHead className="text-center">Assets</TableHead>
              <TableHead className="text-center">Tasks</TableHead>
              <TableHead className="text-center">
                Findings <span className="text-muted-foreground font-normal">C/H/M/L</span>
              </TableHead>
              <TableHead className="text-center">Updated</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading ? (
              <TableRow>
                <TableCell colSpan={5} className="py-10 text-center text-muted-foreground">
                  <Loader2Icon className="mx-auto size-5 animate-spin" />
                </TableCell>
              </TableRow>
            ) : projects.length === 0 ? (
              <TableRow>
                <TableCell colSpan={5} className="py-10 text-center text-muted-foreground">
                  No projects yet. Create one, or just create a task whose goal names a binary (e.g. clfs.sys) — it
                  links automatically once the project exists.
                </TableCell>
              </TableRow>
            ) : (
              projects.map((p) => {
                const f = p.findings;
                const fTotal = f ? f.critical + f.high + f.medium + f.low : 0;
                const seg = (n: number, cls: string) => (
                  <span className={n > 0 ? cls : "text-muted-foreground"}>{n}</span>
                );
                return (
                  <TableRow key={p.id} className="cursor-pointer" onClick={() => (window.location.href = `/function/projects/detail?key=${encodeURIComponent(p.key)}`)}>
                    <TableCell>
                      <div className="flex items-center gap-2">
                        <BoxesIcon className="size-4 text-muted-foreground" />
                        <div>
                          <div className="font-medium">{p.name}</div>
                          <code className="text-xs text-muted-foreground">{p.key}</code>
                        </div>
                      </div>
                    </TableCell>
                    <TableCell className="text-center text-xs tabular-nums">{p.asset_count}</TableCell>
                    <TableCell className="text-center text-xs tabular-nums">
                      <span className={p.tasks_running > 0 ? "font-medium text-blue-600 dark:text-blue-400" : "text-muted-foreground"}>
                        {p.tasks_running}
                      </span>
                      <span className="text-muted-foreground">/</span>
                      <span className={p.tasks_done > 0 ? "font-medium text-emerald-600 dark:text-emerald-400" : "text-muted-foreground"}>
                        {p.tasks_done}
                      </span>
                    </TableCell>
                    <TableCell className="text-center text-xs tabular-nums">
                      {f && fTotal > 0 ? (
                        <span className="font-medium whitespace-nowrap" title="Critical / High / Medium / Low">
                          {seg(f.critical, sevTone.critical)}
                          <span className="text-muted-foreground">/</span>
                          {seg(f.high, sevTone.high)}
                          <span className="text-muted-foreground">/</span>
                          {seg(f.medium, sevTone.medium)}
                          <span className="text-muted-foreground">/</span>
                          {seg(f.low, sevTone.low)}
                        </span>
                      ) : (
                        <span className="text-muted-foreground">0</span>
                      )}
                    </TableCell>
                    <TableCell className="text-center text-xs text-muted-foreground">{fmtTime(p.updated_at)}</TableCell>
                  </TableRow>
                );
              })
            )}
          </TableBody>
        </Table>
      </DataTableShell>

      <Dialog open={showCreate} onOpenChange={setShowCreate}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>New project</DialogTitle>
            <DialogDescription>
              One per target software, named for the whole thing (e.g. “Windows 11 26H2 (build 26300)”). Binaries are
              the file names tasks stage (clfs.sys, afd.sys) — the auto-link and the function map join on them.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-1">
              <Label>Name</Label>
              <Input placeholder="Windows 11 26H2 (build 26300.9974)" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
            </div>
            <div className="grid grid-cols-2 gap-2">
              <div className="space-y-1">
                <Label>Key (optional)</Label>
                <Input placeholder="windows-11-26h2" value={form.key} onChange={(e) => setForm({ ...form, key: e.target.value })} />
              </div>
              <div className="space-y-1">
                <Label>Binaries (comma-separated)</Label>
                <Input placeholder="clfs.sys, afd.sys" value={form.binaries} onChange={(e) => setForm({ ...form, binaries: e.target.value })} />
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
    </div>
  );
}