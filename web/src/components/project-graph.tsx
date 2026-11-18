"use client";

import * as React from "react";
import dynamic from "next/dynamic";

import { Loader2Icon, RefreshCwIcon, SearchIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api, ApiError } from "@/lib/api";

const ForceGraph2D = dynamic(() => import("react-force-graph-2d"), { ssr: false });

type GNode = {
  id: string;
  group: string;
  label: string;
  sub?: string;
  color?: string;
  extra?: Record<string, unknown>;
  x?: number;
  y?: number;
  expanded?: boolean;
  hl?: boolean;
};

type GLink = { source: string | GNode; target: string | GNode; kind: string; hl?: boolean };

export type ProjectGraph = {
  project: { key: string; name: string; binaries: string[] };
  nodes: GNode[];
  edges: { from: string; to: string; kind: string }[];
  crossmap: { function_total?: number; function_nodes?: number; function_edges?: number; binaries_indexed?: number };
};

const GROUP_COLORS: Record<string, string> = {
  project: "#f472b6",
  binary: "#38bdf8",
  finding: "#f87171",
  task: "#4ade80",
  function: "#94a3b8",
  entry: "#facc15",
  import: "#a78bfa",
  ioctl: "#fb923c",
};

const SEV_COLORS: Record<string, string> = {
  critical: "#ef4444",
  high: "#f97316",
  medium: "#eab308",
  low: "#60a5fa",
};

// crossmap helpers -----------------------------------------------------------

async function crossmap(path: string): Promise<Record<string, unknown>> {
  const r = await fetch(`http://172.17.0.1:13060${path}`);
  if (!r.ok) throw new Error(`crossmap ${path}: ${r.status}`);
  return (await r.json()) as Record<string, unknown>;
}

export function ProjectGraphView({ projectKey }: { projectKey: string }) {
  const [graph, setGraph] = React.useState<ProjectGraph | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [selected, setSelected] = React.useState<GNode | null>(null);
  const [chainFrom, setChainFrom] = React.useState("");
  const [chainTo, setChainTo] = React.useState("");
  const [chainPath, setChainPath] = React.useState<string[] | null>(null);
  const fgRef = React.useRef<{ zoomToFit: (ms?: number, px?: number) => void; centerAt: (x: number, y: number, ms?: number) => void } | null>(null);
  const expandedRef = React.useRef<Set<string>>(new Set());

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const g = await api.projectGraph(projectKey);
      setGraph(g);
      setTimeout(() => fgRef.current?.zoomToFit(400, 60), 600);
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, [projectKey]);

  React.useEffect(() => {
    void load();
  }, [load]);

  // expand a binary: fetch its top functions from crossmap, splice them in
  const expandBinary = React.useCallback(
    async (binaryName: string) => {
      if (expandedRef.current.has(binaryName)) return;
      expandedRef.current.add(binaryName);
      try {
        const r = await crossmap(`/map/expand?binary=${encodeURIComponent(binaryName)}&limit=25`);
        const fns = (r.functions as { name: string; in: number; out: number }[]) || [];
        const edges = (r.edges as { from: string; to: string; kind: string }[]) || [];
        setGraph((prev) => {
          if (!prev) return prev;
          const ids = new Set(prev.nodes.map((n) => n.id));
          const add: GNode[] = [];
          for (const f of fns) {
            const id = `fn:${binaryName}!${f.name}`;
            if (!ids.has(id)) {
              add.push({ id, group: "function", label: f.name, sub: `${f.in}↖ ${f.out}↘`, extra: { binary: binaryName } });
            }
          }
          const linkIds = new Set(prev.edges.map((e) => `${e.from}->${e.to}`));
          const addLinks = edges
            .filter((e) => !linkIds.has(`${e.from}->${e.to}`))
            .map((e) => ({ from: e.from, to: e.to, kind: e.kind }));
          return { ...prev, nodes: [...prev.nodes, ...add], edges: [...prev.edges, ...addLinks] };
        });
      } catch (e) {
        toast.error(`expand failed: ${String(e)}`);
        expandedRef.current.delete(binaryName);
      }
    },
    [],
  );

  // chain finder: crossmap /map/paths, highlight the result
  const findChain = React.useCallback(async () => {
    if (!chainFrom.includes("!") || !chainTo.includes("!")) {
      toast.error("from and to must each be binary!function");
      return;
    }
    try {
      const r = await crossmap(
        `/map/paths?from=${encodeURIComponent(chainFrom)}&to=${encodeURIComponent(chainTo)}&max_hops=6`,
      );
      const paths = (r.paths as { hops: { ref: string }[] }[]) || [];
      if (paths.length === 0) {
        toast.info("no path found within 6 hops");
        setChainPath(null);
        return;
      }
      setChainPath(paths[0].hops.map((h) => h.ref));
      toast.success(`path of ${paths[0].hops.length} hops highlighted`);
    } catch (e) {
      toast.error(`chain search failed: ${String(e)}`);
    }
  }, [chainFrom, chainTo]);

  const data = React.useMemo(() => {
    if (!graph) return { nodes: [] as GNode[], links: [] as GLink[] };
    const hlSet = new Set(chainPath ?? []);
    const nodes = graph.nodes.map((n) => ({ ...n, hl: chainPath ? hlSet.has(n.id.startsWith("fn:") ? n.id.slice(3) : n.id) : false }));
    return {
      nodes,
      links: graph.edges.map((e) => {
        const fh = e.from.startsWith("fn:") ? e.from.slice(3) : e.from;
        const th = e.to.startsWith("fn:") ? e.to.slice(3) : e.to;
        const hl = chainPath ? hlSet.has(fh) && hlSet.has(th) : false;
        return { source: e.from, target: e.to, kind: e.kind, hl };
      }),
    };
  }, [graph, chainPath]);

  const nodeColor = (n: GNode) => {
    if (n.group === "finding" && n.color && SEV_COLORS[n.color]) return SEV_COLORS[n.color];
    if (n.hl) return "#22d3ee";
    return GROUP_COLORS[n.group] ?? "#64748b";
  };

  const nodeSize = (n: GNode) => {
    if (n.group === "project") return 12;
    if (n.group === "binary") return 8 + Math.min(8, ((n.extra?.functions as number) ?? 0) / 2000);
    if (n.group === "entry" || n.group === "ioctl") return 7;
    return n.hl ? 7 : 4;
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          {Object.entries(GROUP_COLORS).map(([g, c]) => (
            <span key={g} className="flex items-center gap-1 text-xs text-muted-foreground">
              <span className="inline-block size-2.5 rounded-full" style={{ background: c }} />
              {g}
            </span>
          ))}
          {graph && (graph.crossmap.function_total ?? 0) > 0 && (
            <span className="text-xs text-muted-foreground">
              · {graph.crossmap.function_total?.toLocaleString()} functions indexed
            </span>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Input className="h-8 w-56 font-mono text-xs" placeholder="chain from: binary!fn" value={chainFrom} onChange={(e) => setChainFrom(e.target.value)} />
          <Input className="h-8 w-56 font-mono text-xs" placeholder="chain to: binary!fn" value={chainTo} onChange={(e) => setChainTo(e.target.value)} />
          <Button size="sm" variant="outline" onClick={() => void findChain()}>
            <SearchIcon className="size-3.5" /> Find chain
          </Button>
          <Button variant="outline" size="sm" onClick={() => void load()} disabled={loading}>
            {loading ? <Loader2Icon className="size-3.5 animate-spin" /> : <RefreshCwIcon className="size-3.5" />}
          </Button>
          <Button variant="outline" size="sm" onClick={() => { setChainPath(null); fgRef.current?.zoomToFit(400, 60); }}>
            Fit view
          </Button>
        </div>
      </div>
      <div className="relative flex min-h-0 flex-1 gap-3">
        <div className="min-h-0 flex-1 overflow-hidden rounded-xl border bg-card">
          {!loading && graph ? (
            <ForceGraph2D
              ref={fgRef as never}
              graphData={data as never}
              nodeLabel={(n) => {
                const node = n as GNode;
                return `${node.label}${node.sub ? ` (${node.sub})` : ""}`;
              }}
              nodeColor={(n) => nodeColor(n as GNode)}
              nodeVal={(n) => nodeSize(n as GNode)}
              linkColor={(l) => ((l as GLink).hl ? "#22d3ee" : "#3f4a5a")}
              linkWidth={(l) => ((l as GLink).hl ? 2.5 : 0.6)}
              onNodeClick={(n) => {
                const node = n as GNode;
                setSelected(node);
                if (node.group === "binary") void expandBinary(String((node.extra?.name as string) ?? node.label.split(" ")[0]));
              }}
              cooldownTicks={150}
              height={720}
            />
          ) : (
            <div className="flex h-full items-center justify-center text-muted-foreground">
              <Loader2Icon className="mr-2 size-5 animate-spin" /> loading graph…
            </div>
          )}
        </div>
        {selected && (
          <div className="w-80 shrink-0 space-y-3 overflow-y-auto rounded-xl border bg-card p-4">
            <div>
              <div className="text-xs text-muted-foreground uppercase tracking-wide">{selected.group}</div>
              <div className="mt-1 font-medium leading-snug">{selected.label}</div>
              {selected.sub && (
                <div className="mt-1">
                  <Badge variant="outline">{selected.sub}</Badge>
                </div>
              )}
            </div>
            {selected.extra &&
              Object.entries(selected.extra).map(([k, v]) => (
                <div key={k} className="text-xs">
                  <span className="text-muted-foreground">{k}: </span>
                  <span className="break-all font-mono">{String(v as unknown as string)}</span>
                </div>
              ))}
            {selected.group === "binary" && (
              <div className="space-y-1">
                <div className="text-xs text-muted-foreground">top functions expand on click</div>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => void expandBinary(String((selected.extra?.name as string) ?? selected.label.split(" ")[0]))}
                >
                  {expandedRef.current.has(selected.label.split(" ")[0]) ? "expanded" : "Expand functions"}
                </Button>
              </div>
            )}
            {selected.group === "finding" && selected.extra?.finding_id != null && (
              <Button size="sm" variant="outline" asChild>
                <a href={`/function/findings/detail?id=${String(selected.extra.finding_id)}`}>Open finding</a>
              </Button>
            )}
            {selected.group === "task" && selected.extra?.task_id != null && (
              <Button size="sm" variant="outline" asChild>
                <a href={`/function/tasks/detail?id=${String(selected.extra.task_id)}`}>Open task</a>
              </Button>
            )}
            <Button size="sm" variant="ghost" onClick={() => setSelected(null)}>
              Close
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}