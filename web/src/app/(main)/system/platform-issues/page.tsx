"use client";

import * as React from "react";

import { CheckCircle2Icon, Loader2Icon, PlusIcon, RefreshCwIcon, XCircleIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { DataTableShell } from "@/components/data-table-shell";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, ApiError } from "@/lib/api";
import type { PlatformIssue } from "@/lib/types";

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
}

export default function PlatformIssuesPage() {
  const [rows, setRows] = React.useState<PlatformIssue[]>([]);
  const [loading, setLoading] = React.useState(true);
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

  const open = rows.filter((r) => r.status === "open").length;

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">Platform issues</h1>
          <p className="text-sm text-muted-foreground">
            Defects in ARTEX itself, surfaced by the self-improvement loops (self-critique, contrastive extraction) or
            filed manually. {open} open — these are your fix-later backlog, deliberately kept out of worker lesson
            prompts.
          </p>
        </div>
        <Button variant="outline" onClick={() => void load()} disabled={loading}>
          {loading ? <Loader2Icon className="size-4 animate-spin" /> : <RefreshCwIcon className="size-4" />} Refresh
        </Button>
      </div>

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-base">File an issue</CardTitle>
        </CardHeader>
        <CardContent className="flex gap-2">
          <Input placeholder="title (imperative, e.g. 'ssh_upload fails on Windows targets')" value={title} onChange={(e) => setTitle(e.target.value)} />
          <Input placeholder="detail (optional)" value={detail} onChange={(e) => setDetail(e.target.value)} />
          <Button size="sm" onClick={() => void file()} disabled={!title.trim()}>
            <PlusIcon className="size-4" /> File
          </Button>
        </CardContent>
      </Card>

      <DataTableShell
        fill
        toolbar={<span className="text-sm text-muted-foreground">{rows.length} issues{loading ? " · loading…" : ""}</span>}
      >
        <Table>
          <TableHeader className="sticky top-0 z-10 bg-card">
            <TableRow>
              <TableHead className="w-16">ID</TableHead>
              <TableHead>Title</TableHead>
              <TableHead className="w-32">Source</TableHead>
              <TableHead className="w-24">Status</TableHead>
              <TableHead className="w-36">Updated</TableHead>
              <TableHead className="w-52">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => (
              <React.Fragment key={r.id}>
                <TableRow
                  className="cursor-pointer"
                  onClick={() => setExpanded(expanded === r.id ? null : r.id)}
                >
                  <TableCell className="font-mono text-xs">{r.id}</TableCell>
                  <TableCell className="max-w-[460px] truncate font-medium">{r.title}</TableCell>
                  <TableCell>
                    <Badge variant="outline">{r.source}</Badge>
                  </TableCell>
                  <TableCell>
                    <Badge
                      variant={r.status === "open" ? "default" : "secondary"}
                      className={r.status === "open" ? "bg-amber-500/15 text-amber-700 dark:text-amber-400" : ""}
                    >
                      {r.status}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">{new Date(r.updated_at).toLocaleString()}</TableCell>
                  <TableCell onClick={(e) => e.stopPropagation()}>
                    {r.status === "open" ? (
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
                        reopen
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
                {expanded === r.id && (
                  <TableRow className="hover:bg-transparent">
                    <TableCell colSpan={6}>
                      <pre className="max-h-60 overflow-auto whitespace-pre-wrap rounded-md bg-muted p-3 text-xs">
                        {r.detail || "(no detail)"}
                        {r.source_ref ? `\n\nsource: ${r.source_ref}` : ""}
                        {r.note ? `\n\nnote: ${r.note}` : ""}
                      </pre>
                    </TableCell>
                  </TableRow>
                )}
              </React.Fragment>
            ))}
          </TableBody>
        </Table>
      </DataTableShell>
    </div>
  );
}
