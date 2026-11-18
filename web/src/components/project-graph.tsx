"use client";

import * as React from "react";
import dynamic from "next/dynamic";

import { Loader2Icon, RefreshCwIcon, SearchIcon, XIcon } from "lucide-react";
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
  hl?: boolean;
};

type GLink = { source: string | GNode; target: string | GNode; kind: string; hl?: boolean };

export type ProjectGraph = {
  project: { key: string; name: string; binaries: string[] };
  nodes: GNode[];
  edges: { from: string; to: string; kind: string }[];
  crossmap: { function_total?: number; binaries_indexed?: number };
};

const GROUP_COLORS: Record<string, string> = {
  project: "#f472b6",
  binary: "#38bdf8",
  finding: "#f87171",
  task: "#4ade80",
  function: "#8fa3bf",
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

const CROSSMAP = "http://172.17.0.1:13060";

async function crossmap<T = Record<string, unknown>>(path: string): Promise<T> {
  const r = await fetch(`${CROSSMAP}${path}`);
  if (!r.ok) throw new Error(`crossmap ${path}: ${r.status}`);
  return (await r.json()) as T;
}

// one expanded binary's loaded slice
type ExpandedBin = {
  functions: { name: string; in: number; out: number }[];
  edges: { from: string; to: string; kind: string }[];
};

export function ProjectGraphView({ projectKey }: { projectKey: string }) {
  const [graph, setGraph] = React.useState<ProjectGraph | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [selected, setSelected] = React.useState<GNode | null>(null);
  const [panelBin, setPanelBin] = React.useState<{ name: string; summary: ExpandedBin } | null>(null);
  const [fnFilter, setFnFilter] = React.useState("");
  const [chainFrom, setChainFrom] = React.useState("");
  const [chainTo, setChainTo] = React.useState("");
  const [chainPath, setChainPath] = React.useState<string[] | null>(null);
  const fgRef = React.useRef<{ zoomToFit: (ms?: number, px?: number) => void } | null>(null);
  const expandedRef = React.useRef<Set<string>>(new Set());
  const canvasWrap = React.useRef<HTMLDivElement | null>(null);
  const [canvasH, setCanvasH] = React.useState(700);

  React.useEffect(() => {
    const ro = new ResizeObserver(() => {
      if (canvasWrap.current) setCanvasH(Math.max(420, canvasWrap.current.clientHeight));
    });
    if (canvasWrap.current) ro.observe(canvasWrap.current);
    return () => ro.disconnect();
  }, []);

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

  const loadBinSlice = React.useCallback(async (binaryName: string) => {
    return crossmap<ExpandedBin>(`/map/expand?binary=${encodeURIComponent(binaryName)}&limit=25`);
  }, []);

  // expand binary into the canvas + open its panel
  const openBinary = React.useCallback(
    async (node: GNode) => {
      const binName = String(node.extra?.name ?? node.label.split(" ")[0]);
      setSelected(node);
      setFnFilter("");
      try {
        const sum = await loadBinSlice(binName);
        setPanelBin({ name: binName, summary: sum });
        if (expandedRef.current.has(binName)) return;
        expandedRef.current.add(binName);
        setGraph((prev) => {
          if (!prev) return prev;
          const ids = new Set(prev.nodes.map((n) => n.id));
          const add: GNode[] = [];
          for (const f of sum.functions) {
            const id = `fn:${binName}!${f.name}`;
            if (!ids.has(id)) {
              add.push({ id, group: "function", label: f.name, sub: `${f.in}↖ ${f.out}↘`, extra: { binary: binName } });
            }
          }
          const have = new Set(prev.edges.map((e) => `${e.from}->${e.to}`));
          const addLinks = sum.edges
            .filter((e) => !have.has(`${e.from}->${e.to}`))
            .map((e) => ({ from: e.from, to: e.to, kind: e.kind }));
          return { ...prev, nodes: [...prev.nodes, ...add], edges: [...prev.edges, ...addLinks] };
        });
      } catch (e) {
        toast.error(`expand failed: ${String(e)}`);
      }
    },
    [loadBinSlice],
  );

  const findChain = React.useCallback(async () => {
    if (!chainFrom.includes("!") || !chainTo.includes("!")) {
      toast.error("from/to must each be binary!function (e.g. CLFS.sys!ClfsAddLogContainer)");
      return;
    }
    try {
      const r = await crossmap<{ paths: { hops: { ref: string; via?: string }[] }[] }>(
        `/map/paths?from=${encodeURIComponent(chainFrom)}&to=${encodeURIComponent(chainTo)}&max_hops=6`,
      );
      if (!r.paths?.length) {
        toast.info("no path within 6 hops");
        setChainPath(null);
        return;
      }
      setChainPath(r.paths[0].hops.map((h) => h.ref));
      toast.success(`${r.paths[0].hops.length}-hop path highlighted`);
    } catch (e) {
      toast.error(`chain search failed: ${String(e)}`);
    }
  }, [chainFrom, chainTo]);

  const data = React.useMemo(() => {
    if (!graph) return { nodes: [] as GNode[], links: [] as GLink[] };
    const hlSet = new Set(chainPath ?? []);
    const strip = (id: string) => (id.startsWith("fn:") ? id.slice(3) : id);
    return {
      nodes: graph.nodes.map((n) => ({ ...n, hl: chainPath ? hlSet.has(strip(n.id)) : false })),
      links: graph.edges.map((e) => ({
        source: e.from,
        target: e.to,
        kind: e.kind,
        hl: chainPath ? hlSet.has(strip(e.from)) && hlSet.has(strip(e.to)) : false,
      })),
    };
  }, [graph, chainPath]);

  const nodeColor = (n: GNode) => {
    if (n.hl) return "#22d3ee";
    if (n.group === "finding" && n.color && SEV_COLORS[n.color]) return SEV_COLORS[n.color];
    return GROUP_COLORS[n.group] ?? "#64748b";
  };

  const nodeSize = (n: GNode) => {
    if (n.group === "project") return 14;
    if (n.group === "binary") return 9 + Math.min(9, ((n.extra?.functions as number) ?? 0) / 2000);
    if (n.group === "entry" || n.group === "ioctl") return 7;
    return n.hl ? 7 : 4;
  };

  // draw labels directly on the canvas for the top-level groups
  const nodeCanvasObject = React.useCallback((node: GNode, ctx: CanvasRenderingContext2D, globalScale: number) => {
    const r = nodeSize(node);
    ctx.beginPath();
    ctx.arc(node.x ?? 0, node.y ?? 0, r, 0, 2 * Math.PI);
    ctx.fillStyle = nodeColor(node);
    ctx.fill();
    if (node.hl) {
      ctx.strokeStyle = "#22d3ee";
      ctx.lineWidth = 2.5;
      ctx.stroke();
    }
    // label: top-level nodes always; functions only when zoomed in
    const top = node.group !== "function";
    if (top || globalScale > 1.6) {
      const label = node.label.length > 28 ? `${node.label.slice(0, 27)}…` : node.label;
      ctx.font = `${top ? 4 : 3}px ui-sans-serif, sans-serif`;
      ctx.fillStyle = "rgba(226,232,240,0.92)";
      ctx.textAlign = "center";
      ctx.textBaseline = "top";
      ctx.fillText(label, node.x ?? 0, (node.y ?? 0) + r + 1.5);
    }
  }, [chainPath]);

  const filteredFns = React.useMemo(() => {
    if (!panelBin) return [];
    const q = fnFilter.trim().toLowerCase();
    const fns = panelBin.summary.functions;
    if (!q) return fns;
    return fns.filter((f) => f.name.toLowerCase().includes(q));
  }, [panelBin, fnFilter]);

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
              · {graph.crossmap.function_total?.toLocaleString()} functions indexed · click a binary to expand
            </span>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Input className="h-8 w-52 font-mono text-xs" placeholder="from: binary!fn" value={chainFrom} onChange={(e) => setChainFrom(e.target.value)} />
          <Input className="h-8 w-52 font-mono text-xs" placeholder="to: binary!fn" value={chainTo} onChange={(e) => setChainTo(e.target.value)} />
          <Button size="sm" variant="outline" onClick={() => void findChain()}>
            <SearchIcon className="size-3.5" /> Chain
          </Button>
          <Button variant="outline" size="sm" onClick={() => void load()} disabled={loading}>
            {loading ? <Loader2Icon className="size-3.5 animate-spin" /> : <RefreshCwIcon className="size-3.5" />}
          </Button>
          <Button variant="outline" size="sm" onClick={() => { setChainPath(null); fgRef.current?.zoomToFit(400, 60); }}>
            Fit
          </Button>
        </div>
      </div>
      <div className="relative flex min-h-0 flex-1 gap-3">
        <div ref={canvasWrap} className="min-h-0 flex-1 overflow-hidden rounded-xl border bg-card">
          {!loading && graph ? (
            <ForceGraph2D
              ref={fgRef as never}
              graphData={data as never}
              nodeCanvasObject={(n, ctx, scale) => nodeCanvasObject(n as GNode, ctx, scale)}
              nodePointerAreaPaint={(n, color, ctx) => {
                const node = n as GNode;
                ctx.beginPath();
                ctx.arc(node.x ?? 0, node.y ?? 0, nodeSize(node) + 3, 0, 2 * Math.PI);
                ctx.fillStyle = color;
                ctx.fill();
              }}
              onNodeClick={(n) => {
                const node = n as GNode;
                if (node.group === "binary") void openBinary(node);
                else setSelected(node);
              }}
              cooldownTicks={150}
              height={canvasH}
            />
          ) : (
            <div className="flex h-full items-center justify-center text-muted-foreground">
              <Loader2Icon className="mr-2 size-5 animate-spin" /> loading graph…
            </div>
          )}
        </div>

        {/* side panel */}
        <div className="w-96 shrink-0 space-y-3 overflow-y-auto rounded-xl border bg-card p-4 text-sm">
          {!selected && !panelBin && (
            <div className="py-8 text-center text-muted-foreground text-xs">
              click a node — binaries expand their functions and show the hub list here
            </div>
          )}

          {panelBin && (
            <div className="space-y-2">
              <div className="flex items-start justify-between gap-2">
                <div>
                  <div className="text-xs text-muted-foreground uppercase tracking-wide">binary · {selected?.label}</div>
                  <div className="font-mono text-sm font-semibold">{panelBin.name}</div>
                </div>
                <Button size="icon-sm" variant="ghost" onClick={() => { setPanelBin(null); setSelected(null); }}>
                  <XIcon />
                </Button>
              </div>
              <div className="flex items-center gap-2">
                <Input
                  className="h-8 text-xs"
                  placeholder={`filter ${panelBin.summary.functions.length} functions…`}
                  value={fnFilter}
                  onChange={(e) => setFnFilter(e.target.value)}
                />
              </div>
              <div className="max-h-[60vh] space-y-1 overflow-y-auto pr-1">
                {filteredFns.map((f) => (
                  <button
                    key={f.name}
                    type="button"
                    className="flex w-full items-center justify-between rounded-md border px-2 py-1.5 text-left font-mono text-xs hover:bg-accent"
                    onClick={() => setChainFrom(`${panelBin.name}!${f.name}`)}
                    title="set as chain start"
                  >
                    <span className="min-w-0 truncate">{f.name}</span>
                    <span className="ml-2 shrink-0 text-muted-foreground">
                      ↖{f.in} ↘{f.out}
                    </span>
                  </button>
                ))}
                {filteredFns.length === 0 && <div className="py-4 text-center text-xs text-muted-foreground">no match</div>}
              </div>
              <div className="text-[11px] text-muted-foreground">
                click a function to set it as the chain start; {panelBin.summary.edges.length} internal edges shown on canvas
              </div>
            </div>
          )}

          {selected && !panelBin && (
            <div className="space-y-3">
              <div className="flex items-start justify-between gap-2">
                <div>
                  <div className="text-xs text-muted-foreground uppercase tracking-wide">{selected.group}</div>
                  <div className="mt-1 font-medium leading-snug">{selected.label}</div>
                  {selected.sub && <Badge variant="outline" className="mt-1">{selected.sub}</Badge>}
                </div>
                <Button size="icon-sm" variant="ghost" onClick={() => setSelected(null)}>
                  <XIcon />
                </Button>
              </div>
              {selected.extra &&
                Object.entries(selected.extra).map(([k, v]) => (
                  <div key={k} className="text-xs">
                    <span className="text-muted-foreground">{k}: </span>
                    <span className="break-all font-mono">{String(v as unknown as string)}</span>
                  </div>
                ))}
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
              {selected.group === "binary" && (
                <Button size="sm" variant="outline" onClick={() => void openBinary(selected)}>
                  Expand functions
                </Button>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}