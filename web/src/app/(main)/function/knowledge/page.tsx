"use client";

import * as React from "react";

import { ArrowLeftIcon, DownloadIcon, Loader2Icon, RefreshCwIcon, SearchIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { DataTableShell } from "@/components/data-table-shell";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, ApiError } from "@/lib/api";
import type { KnowledgeEdge, KnowledgeNode } from "@/lib/types";

const KINDS = ["", "primitive", "technique", "crash_signature", "binary", "function", "environment", "lesson"];

const kindColor: Record<string, string> = {
  primitive: "bg-red-500/15 text-red-700 dark:text-red-400",
  technique: "bg-blue-500/15 text-blue-700 dark:text-blue-400",
  crash_signature: "bg-orange-500/15 text-orange-700 dark:text-orange-400",
  binary: "bg-slate-500/15 text-slate-700 dark:text-slate-400",
  function: "bg-violet-500/15 text-violet-700 dark:text-violet-400",
  environment: "bg-emerald-500/15 text-emerald-700 dark:text-emerald-400",
  lesson: "bg-yellow-500/15 text-yellow-700 dark:text-yellow-400",
};

function fmtTime(s: string) {
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleString();
}

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
}

export default function KnowledgePage() {
  const [nodes, setNodes] = React.useState<KnowledgeNode[]>([]);
  const [stats, setStats] = React.useState<{ kinds: Record<string, number>; domains: Record<string, number> }>({ kinds: {}, domains: {} });
  const [loading, setLoading] = React.useState(true);
  const [q, setQ] = React.useState("");
  const [kind, setKind] = React.useState("");
  const [domain, setDomain] = React.useState("");
  const [selected, setSelected] = React.useState<KnowledgeNode | null>(null);
  const [detail, setDetail] = React.useState<{ neighbors: KnowledgeNode[]; edges: KnowledgeEdge[] } | null>(null);
  const [detailLoading, setDetailLoading] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const [ns, st] = await Promise.all([
        api.knowledgeNodes({ q, kind, domain, limit: 200 }),
        api.knowledgeStats(),
      ]);
      setNodes(ns);
      setStats(st);
    } catch (e) {
      toast.error(errText(e));
    } finally {
      setLoading(false);
    }
  }, [q, kind, domain]);

  React.useEffect(() => {
    void load();
  }, [load]);

  // debounce the search box
  const [search, setSearch] = React.useState("");
  React.useEffect(() => {
    const t = setTimeout(() => setQ(search), 300);
    return () => clearTimeout(t);
  }, [search]);

  async function openNode(n: KnowledgeNode) {
    setSelected(n);
    setDetail(null);
    setDetailLoading(true);
    try {
      const d = await api.knowledgeNode(n.id);
      setDetail({ neighbors: d.neighbors ?? [], edges: d.edges ?? [] });
    } catch (e) {
      toast.error(errText(e));
    } finally {
      setDetailLoading(false);
    }
  }

  const kinds = stats.kinds ?? {};
  const domains = stats.domains ?? {};
  const total = Object.values(kinds).reduce((a, b) => a + b, 0);

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">Knowledge graph</h1>
          <p className="text-sm text-muted-foreground">
            Cross-task exploit-craft memory: primitives, techniques, crash signatures and environment facts the agents
            accumulate (kn_add/kn_search). {total} node{total === 1 ? "" : "s"}.
          </p>
        </div>
        <div className="flex gap-2">
          <a href={api.knowledgeVaultUrl()} download>
            <Button variant="outline">
              <DownloadIcon className="size-4" /> Obsidian vault
            </Button>
          </a>
          <Button variant="outline" onClick={() => void load()} disabled={loading}>
            {loading ? <Loader2Icon className="size-4 animate-spin" /> : <RefreshCwIcon className="size-4" />}
            Refresh
          </Button>
        </div>
      </div>

      {Object.keys(domains).length > 0 && (
        <div className="flex flex-wrap gap-2">
          <span className="text-sm text-muted-foreground">domain:</span>
          {Object.entries(domains)
            .sort((a, b) => b[1] - a[1])
            .map(([k, n]) => (
              <button key={k} type="button" onClick={() => setDomain(domain === k ? "" : k)} className="cursor-pointer">
                <Badge variant={domain === k ? "default" : "secondary"} className="gap-1">
                  {k} · {n}
                </Badge>
              </button>
            ))}
        </div>
      )}

      {Object.keys(kinds).length > 0 && (
        <div className="flex flex-wrap gap-2">
          <span className="text-sm text-muted-foreground">kind:</span>
          {Object.entries(kinds)
            .sort((a, b) => b[1] - a[1])
            .map(([k, n]) => (
              <button key={k} type="button" onClick={() => setKind(kind === k ? "" : k)} className="cursor-pointer">
                <Badge variant={kind === k ? "default" : "secondary"} className="gap-1">
                  {k} · {n}
                </Badge>
              </button>
            ))}
        </div>
      )}

      {selected ? (
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0">
            <CardTitle className="flex items-center gap-2 text-base">
              <Button variant="ghost" size="sm" onClick={() => setSelected(null)}>
                <ArrowLeftIcon className="size-4" />
              </Button>
              <Badge className={kindColor[selected.kind] ?? ""}>{selected.kind}</Badge>
              {selected.domain && <Badge variant="outline">{selected.domain}</Badge>}
              <span>{selected.name}</span>
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            {selected.body && <p className="whitespace-pre-wrap text-sm">{selected.body}</p>}
            {selected.tags.length > 0 && (
              <div className="flex flex-wrap gap-1">
                {selected.tags.map((t) => (
                  <Badge key={t} variant="outline">
                    {t}
                  </Badge>
                ))}
              </div>
            )}
            {Object.keys(selected.refs).length > 0 && (
              <div className="text-sm">
                <span className="text-muted-foreground">refs: </span>
                {Object.entries(selected.refs).map(([k, v]) => (
                  <Badge key={k} variant="secondary" className="mr-1">
                    {k}: {String(v)}
                  </Badge>
                ))}
              </div>
            )}
            <div className="text-xs text-muted-foreground">
              dedupe {selected.dedupe_key} · source {selected.source}
              {selected.task_id ? ` · task ${selected.task_id}` : ""} · updated {fmtTime(selected.updated_at)}
            </div>

            <div>
              <h3 className="mb-1 text-sm font-medium">Edges</h3>
              {detailLoading ? (
                <div className="flex items-center gap-2 text-sm text-muted-foreground">
                  <Loader2Icon className="size-4 animate-spin" /> loading neighbors…
                </div>
              ) : detail && detail.edges.length > 0 ? (
                <div className="space-y-1">
                  {detail.edges.map((e) => {
                    const other = e.src === selected.id ? e.dst : e.src;
                    const otherNode = detail.neighbors.find((n) => n.id === other);
                    const dir = e.src === selected.id ? "→" : "←";
                    return (
                      <button
                        key={e.id}
                        type="button"
                        className="block w-full cursor-pointer text-left"
                        onClick={() => otherNode && void openNode(otherNode)}
                      >
                        <span className="font-mono text-xs text-muted-foreground">{dir} {e.rel}</span>{" "}
                        <span className="text-sm underline decoration-dotted">{otherNode?.name ?? `node ${other}`}</span>
                      </button>
                    );
                  })}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">no edges</p>
              )}
            </div>
          </CardContent>
        </Card>
      ) : (
        <Card>
          <CardContent className="pt-4">
            <div className="mb-3 flex gap-2">
              <div className="relative flex-1">
                <SearchIcon className="absolute top-1/2 left-2 size-4 -translate-y-1/2 text-muted-foreground" />
                <Input
                  className="pl-8"
                  placeholder="search primitives, techniques, crash signatures…"
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                />
              </div>
              <select
                className="h-9 rounded-md border bg-transparent px-2 text-sm"
                value={kind}
                onChange={(e) => setKind(e.target.value)}
              >
                {KINDS.map((k) => (
                  <option key={k} value={k}>
                    {k === "" ? "all kinds" : k}
                  </option>
                ))}
              </select>
              <select
                className="h-9 rounded-md border bg-transparent px-2 text-sm"
                value={domain}
                onChange={(e) => setDomain(e.target.value)}
              >
                {["", "memory", "web", "crypto", "access", "general"].map((dm) => (
                  <option key={dm} value={dm}>
                    {dm === "" ? "all domains" : dm}
                  </option>
                ))}
              </select>
            </div>
            <DataTableShell
              toolbar={
                <div className="flex w-full flex-wrap items-center gap-2">
                  <span className="text-sm text-muted-foreground">
                    {nodes.length} node{nodes.length === 1 ? "" : "s"}
                    {loading ? " · loading…" : ""}
                  </span>
                </div>
              }
            >
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Kind</TableHead>
                    <TableHead>Name</TableHead>
                    <TableHead>Tags</TableHead>
                    <TableHead>Refs</TableHead>
                    <TableHead>Task</TableHead>
                    <TableHead>Updated</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {nodes.map((n) => (
                    <TableRow key={n.id} className="cursor-pointer" onClick={() => void openNode(n)}>
                      <TableCell>
                        <div className="flex flex-col gap-1">
                          <Badge className={kindColor[n.kind] ?? ""}>{n.kind}</Badge>
                          {n.domain && (
                            <Badge variant="outline" className="text-[10px]">
                              {n.domain}
                            </Badge>
                          )}
                        </div>
                      </TableCell>
                      <TableCell className="max-w-[380px] truncate font-medium">{n.name}</TableCell>
                      <TableCell className="max-w-[180px] truncate text-xs text-muted-foreground">
                        {n.tags.join(", ") || "—"}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {Object.entries(n.refs)
                          .map(([k, v]) => `${k}:${String(v)}`)
                          .join(" ") || "—"}
                      </TableCell>
                      <TableCell>{n.task_id ?? "—"}</TableCell>
                      <TableCell className="text-xs text-muted-foreground">{fmtTime(n.updated_at)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </DataTableShell>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
