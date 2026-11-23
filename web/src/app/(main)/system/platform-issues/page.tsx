"use client";

import * as React from "react";

import {
  CheckCircle2Icon,
  CircleDashedIcon,
  Loader2Icon,
  PlusIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  XCircleIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { DataTableShell } from "@/components/data-table-shell";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, ApiError } from "@/lib/api";
import type { PlatformIssue } from "@/lib/types";

type Filter = "open" | "closed" | "all";

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
}

export default function PlatformIssuesPage() {
  const [rows, setRows] = React.useState<PlatformIssue[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [filter, setFilter] = React.useState<Filter>("open");
  const [title, setTitle] = React.useState("");
  const [detail, setDetail] = React.useState("");
  const [expanded, setExpanded] = React.useState<number | null>(null);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      setRows(await api.platformIssues());
    } catch (e) {
      toast.error(errText(e));
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  async function file() {
    if (!title.trim()) return;
    try {
      await api.createPlatformIssue({ title: title.trim(), detail: detail.trim() });
      setTitle("");
      setDetail("");
      toast.success("issue filed");
      setFilter("open");
      await load();
    } catch (e) {
      toast.error(errText(e));
    }
  }

  async function setStatus(id: number, status: "resolved" | "wontfix" | "open") {
    try {
      await api.updatePlatformIssue(id, { status, note: status === "open" ? "" : "from issues page" });
      await load();
    } catch (e) {
      toast.error(errText(e));
    }
  }

  const openCount = rows.filter((r) => r.status === "open").length;
  const closedCount = rows.length - openCount;
  const visible = rows.filter((r) => (filter === "all" ? true : filter === "open" ? r.status === "open" : r.status !== "open"));

  const filters: { key: Filter; label: string; count: number; icon: React.ReactNode }[] = [
    { key: "open", label: "Open", count: openCount, icon: <CircleDashedIcon className="size-3.5" /> },
    { key: "closed", label: "Resolved", count: closedCount, icon: <CheckCircle2Icon className="size-3.5" /> },
    { key: "all", label: "All", count: rows.length, icon: null },
  ];

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">Platform issues</h1>
          <p className="text-sm text-muted-foreground">
            Defects in ARTEX itself, surfaced by the self-improvement loops or filed manually — your fix-later backlog,
            kept out of worker lesson prompts.
          </p>
        </div>
        <Button variant="outline" onClick={() => void load()} disabled={loading}>
          {loading ? <Loader2Icon className="size-4 animate-spin" /> : <RefreshCwIcon className="size-4" />} Refresh
        </Button>
      </div>

      {/* GitHub-style filter bar: counts per state, defaults to open */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex overflow-hidden rounded-md border">
          {filters.map((f) => (
            <button
              key={f.key}
              type="button"
              onClick={() => setFilter(f.key)}
              className={`flex cursor-pointer items-center gap-1.5 px-3 py-1.5 text-sm ${
                filter === f.key ? "bg-primary text-primary-foreground" : "bg-background text-muted-foreground hover:bg-muted"
              }`}
            >
              {f.icon}
              {f.label}
              <span
                className={`rounded-full px-1.5 text-xs ${
                  filter === f.key ? "bg-primary-foreground/20 text-primary-foreground" : "bg-muted text-muted-foreground"
                }`}
              >
                {f.count}
              </span>
            </button>
          ))}
        </div>
        <span className="ml-auto text-xs text-muted-foreground">
          {visible.length} {filter === "all" ? "total" : filter === "open" ? "open" : "resolved"}
        </span>
      </div>

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-base">File an issue</CardTitle>
        </CardHeader>
        <CardContent className="flex gap-2">
          <Input
            placeholder="title (imperative, e.g. 'ssh_upload fails on Windows targets')"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void file();
            }}
          />
          <Input placeholder="detail (optional)" value={detail} onChange={(e) => setDetail(e.target.value)} />
          <Button size="sm" onClick={() => void file()} disabled={!title.trim()}>
            <PlusIcon className="size-4" /> New issue
          </Button>
        </CardContent>
      </Card>

      <DataTableShell fill toolbar={null}>
        <Table>
          <TableHeader className="sticky top-0 z-10 bg-card">
            <TableRow>
              <TableHead className="w-12"></TableHead>
              <TableHead className="w-16">ID</TableHead>
              <TableHead>Title</TableHead>
              <TableHead className="w-32">Source</TableHead>
              <TableHead className="w-36">Updated</TableHead>
              <TableHead className="w-52">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {visible.length === 0 && !loading && (
              <TableRow>
                <TableCell colSpan={6} className="py-10 text-center text-sm text-muted-foreground">
                  {filter === "open" ? (
                    <>
                      <CheckCircle2Icon className="mx-auto mb-2 size-6 text-emerald-600" />
                      No open issues. Nice.
                    </>
                  ) : (
                    "Nothing here."
                  )}
                </TableCell>
              </TableRow>
            )}
            {visible.map((r) => {
              const isOpen = r.status === "open";
              return (
                <React.Fragment key={r.id}>
                  <TableRow
                    className={`cursor-pointer ${isOpen ? "" : "text-muted-foreground"}`}
                    onClick={() => setExpanded(expanded === r.id ? null : r.id)}
                  >
                    <TableCell>
                      {isOpen ? (
                        <CircleDashedIcon className="size-4 text-amber-600" />
                      ) : r.status === "wontfix" ? (
                        <XCircleIcon className="size-4 text-muted-foreground" />
                      ) : (
                        <CheckCircle2Icon className="size-4 text-emerald-600" />
                      )}
                    </TableCell>
                    <TableCell className="font-mono text-xs">#{r.id}</TableCell>
                    <TableCell className="max-w-[460px]">
                      <div className={`truncate ${isOpen ? "font-medium" : "line-through decoration-muted-foreground/40"}`}>
                        {r.title}
                      </div>
                      {!isOpen && r.note && (
                        <div className="mt-0.5 truncate text-xs text-muted-foreground">{r.note}</div>
                      )}
                    </TableCell>
                    <TableCell>
                      <Badge variant="outline">{r.source}</Badge>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">{new Date(r.updated_at).toLocaleString()}</TableCell>
                    <TableCell onClick={(e) => e.stopPropagation()}>
                      {isOpen ? (
                        <div className="flex gap-1">
                          <Button variant="ghost" size="sm" onClick={() => void setStatus(r.id, "resolved")}>
                            <CheckCircle2Icon className="size-4 text-emerald-600" /> resolve
                          </Button>
                          <Button variant="ghost" size="sm" onClick={() => void setStatus(r.id, "wontfix")}>
                            <XCircleIcon className="size-4" /> wontfix
                          </Button>
                        </div>
                      ) : (
                        <Button variant="ghost" size="sm" onClick={() => void setStatus(r.id, "open")}>
                          <RotateCcwIcon className="size-4" /> reopen
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                  {expanded === r.id && (
                    <TableRow className="hover:bg-transparent">
                      <TableCell colSpan={6}>
                        <div className="space-y-3 rounded-md bg-muted p-3 text-xs">
                          <pre className="max-h-60 overflow-auto whitespace-pre-wrap font-mono">{r.detail || "(no detail)"}</pre>
                          <div className="flex flex-wrap gap-4 text-muted-foreground">
                            {r.source_ref && <span>source: <span className="font-mono">{r.source_ref}</span></span>}
                            <span>opened: {new Date(r.created_at).toLocaleString()}</span>
                            {r.note && <span>resolution: {r.note}</span>}
                          </div>
                        </div>
                      </TableCell>
                    </TableRow>
                  )}
                </React.Fragment>
              );
            })}
          </TableBody>
        </Table>
      </DataTableShell>
    </div>
  );
}
