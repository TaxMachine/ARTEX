"use client";

import * as React from "react";
import dynamic from "next/dynamic";

import { ChevronDownIcon, ChevronUpIcon, Loader2Icon, RefreshCwIcon, SearchIcon, XIcon } from "lucide-react";
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

type ExpandedBin = {
  functions: { name: string; in: number; out: number }[];
  edges: { from: string; to: string; kind: string }[];
};

export function ProjectGraphView({ projectKey }: { projectKey: string }) {
  const [graph, setGraph] = React.useState<ProjectGraph | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [selected, setSelected] = React.useState<GNode | null>(null);
  const [panelBin, setPanelBin] = React.useState<{ name: string; node: GNode; summary: ExpandedBin } | null>(null);
  const [fnFilter, setFnFilter] = React.useState("");
  const [showGenerations, setShowGenerations] = React.useState(false);
  const [chainFrom, setChainFrom] = React.useState("");
  const [chainTo, setChainTo] = React.useState("");
  const [chainPath, setChainPath] = React.useState<string[] | null>(null);
  const fgRef = React.useRef<{ zoomToFit: (ms?: number, px?: number) => void } | null>(null);
  const expandedRef = React.useRef<Set<string>>(new Set());
  const canvasWrap = React.useRef<HTMLDivElement | null>(null);
  const [dim, setDim] = React.useState({ w: 900, h: 650 });
  const [hover, setHover] = React.useState<GNode | null>(null);

  React.useEffect(() => {
    const ro = new ResizeObserver(() => {
      if (canvasWrap.current) {
        setDim({ w: canvasWrap.current.clientWidth, h: Math.max(420, canvasWrap.current.clientHeight) });
      }
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

  const loadBinSlice = React.useCallback(
    async (binaryName: string) => crossmap<ExpandedBin>(`/map/expand?binary=${encodeURIComponent(binaryName)}&limit=25`),
    [],
  );

  const openBinary = React.useCallback(
    async (node: GNode) => {
      const binName = String(node.extra?.name ?? node.label.split(" ")[0]);
      setSelected(node);
      setFnFilter("");
      try {
        const sum = await loadBinSlice(binName);
        setPanelBin({ name: binName, node, summary: sum });
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
      toast.error("from/to must each be binary!function");
      return;
    }
    try {
      const r = await crossmap<{ paths: { hops: { ref: string }[] }[] }>(
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

  // initial view: hide non-latest binary generations unless toggled
  const data = React.useMemo(() => {
    if (!graph) return { nodes: [] as GNode[], links: [] as GLink[] };
    const hlSet = new Set(chainPath ?? []);
    const strip = (id: string) => (id.startsWith("fn:") ? id.slice(3) : id);
    const visible = graph.nodes.filter((n) => {
      if (n.group === "binary" && !showGenerations && n.extra?.is_latest === false) return false;
      return true;
    });
    const visIds = new Set(visible.map((n) => n.id));
    return {
      nodes: visible.map((n) => ({ ...n, hl: chainPath ? hlSet.has(strip(n.id)) : false })),
      links: graph.edges
        .filter((e) => visIds.has(e.from) && visIds.has(e.to))
        .map((e) => ({
          source: e.from,
          target: e.to,
          kind: e.kind,
          hl: chainPath ? hlSet.has(strip(e.from)) && hlSet.has(strip(e.to)) : false,
        })),
    };
  }, [graph, chainPath, showGenerations]);

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

  // info-dense node painter: label + key stats under every top-level node
  const nodeCanvasObject = React.useCallback(
    (node: GNode, ctx: CanvasRenderingContext2D, globalScale: number) => {
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
      const top = node.group !== "function";
      if (top || globalScale > 1.6) {
        const x = node.x ?? 0;
        const y = node.y ?? 0;
        ctx.textAlign = "center";
        // title line
        const title = node.label.length > 26 ? `${node.label.slice(0, 25)}…` : node.label;
        ctx.font = "4.2px ui-sans-serif, sans-serif";
        ctx.fillStyle = "rgba(232,238,247,0.95)";
        ctx.textBaseline = "top";
        ctx.fillText(title, x, y + r + 1.5);
        // stats line: what the node carries
        let stat = "";
        if (node.group === "binary") {
          const f = node.extra?.functions as number | undefined;
          const gen = node.extra?.version ? `v${String(node.extra.version).split(".").pop()}` : "";
          const fcount = node.extra?.findings as number | undefined;
          stat = [f != null ? `${f.toLocaleString()} fns` : "", gen, fcount ? `${fcount} findings` : ""]
            .filter(Boolean)
            .join(" · ");
        } else if (node.group === "finding") {
          stat = `${node.color ?? ""} · ${String(node.extra?.status ?? "")}`;
        } else if (node.group === "task") {
          stat = String(node.extra?.status ?? "");
        } else if (node.group === "function") {
          stat = node.sub ?? "";
        }
        if (stat) {
          ctx.font = "3px ui-sans-serif, sans-serif";
          ctx.fillStyle = "rgba(148,163,184,0.95)";
          ctx.fillText(stat, x, y + r + 6.5);
        }
      }
    },
    [chainPath],
  );

  const filteredFns = React.useMemo(() => {
    if (!panelBin) return [];
    const q = fnFilter.trim().toLowerCase();
    if (!q) return panelBin.summary.functions;
    return panelBin.summary.functions.filter((f) => f.name.toLowerCase().includes(q));
  }, [panelBin, fnFilter]);

  const olderGens = graph
    ? graph.nodes.filter((n) => n.group === "binary" && n.extra?.is_latest === false)
    : [];

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      {/* compact toolbar */}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          {Object.entries(GROUP_COLORS).map(([g, c]) => (
            <span key={g} className="flex items-center gap-1">
              <span className="inline-block size-2.5 rounded-full" style={{ background: c }} />
              {g}
            </span>
          ))}
          {graph && (graph.crossmap.function_total ?? 0) > 0 && (
            <span>· {graph.crossmap.function_total?.toLocaleString()} fns indexed</span>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {olderGens.length > 0 && (
            <Button size="sm" variant="outline" onClick={() => setShowGenerations((v) => !v)}>
              {showGenerations ? <ChevronUpIcon className="size-3.5" /> : <ChevronDownIcon className="size-3.5" />}
              {showGenerations ? "hide" : "show"} {olderGens.length} older gen
            </Button>
          )}
          <Input className="h-8 w-48 font-mono text-xs" placeholder="from: binary!fn" value={chainFrom} onChange={(e) => setChainFrom(e.target.value)} />
          <Input className="h-8 w-48 font-mono text-xs" placeholder="to: binary!fn" value={chainTo} onChange={(e) => setChainTo(e.target.value)} />
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

      {/* canvas fills the page; panel floats over it */}
      <div ref={canvasWrap} className="relative min-h-0 flex-1 overflow-hidden rounded-xl border bg-card">
        {!loading && graph ? (
          <>
            <ForceGraph2D
              ref={fgRef as never}
              graphData={data as never}
              width={dim.w}
              height={dim.h}
              nodeCanvasObject={(n, ctx, scale) => nodeCanvasObject(n as GNode, ctx, scale)}
              nodePointerAreaPaint={(n, color, ctx) => {
                const node = n as GNode;
                ctx.beginPath();
                ctx.arc(node.x ?? 0, node.y ?? 0, nodeSize(node) + 4, 0, 2 * Math.PI);
                ctx.fillStyle = color;
                ctx.fill();
              }}
              onNodeHover={(n) => setHover((n as GNode) ?? null)}
              onNodeClick={(n) => {
                const node = n as GNode;
                if (node.group === "binary") void openBinary(node);
                else setSelected(node);
              }}
              cooldownTicks={150}
            />
            {hover && (
              <div className="pointer-events-none absolute left-2 top-2 max-w-md rounded-lg border bg-background/90 p-2 text-xs backdrop-blur">
                <div className="font-medium">{hover.label}</div>
                <div className="text-muted-foreground">
                  {hover.group}
                  {hover.sub ? ` · ${hover.sub}` : ""}
                  {hover.extra?.functions != null ? ` · ${Number(hover.extra.functions).toLocaleString()} functions` : ""}
                </div>
              </div>
            )}
          </>
        ) : (
          <div className="flex h-full items-center justify-center text-muted-foreground">
            <Loader2Icon className="mr-2 size-5 animate-spin" /> loading graph…
          </div>
        )}

        {/* floating detail panel (right side, over the canvas) */}
        {(panelBin || selected) && (
          <div className="absolute right-2 top-2 bottom-2 w-96 overflow-y-auto rounded-xl border bg-background/95 p-4 text-sm shadow-lg backdrop-blur">
            {panelBin ? (
              <div className="space-y-2">
                <div className="flex items-start justify-between gap-2">
                  <div>
                    <div className="text-[11px] text-muted-foreground uppercase tracking-wide">
                      binary · {panelBin.node.label}
                    </div>
                    <div className="font-mono text-sm font-semibold">{panelBin.name}</div>
                    <div className="mt-1 flex flex-wrap gap-1">
                      {panelBin.node.extra?.functions != null && (
                        <Badge variant="secondary">{Number(panelBin.node.extra.functions).toLocaleString()} fns</Badge>
                      )}
                      {panelBin.node.extra?.version ? <Badge variant="outline">v{String(panelBin.node.extra.version)}</Badge> : null}
                      {panelBin.node.extra?.os_build ? <Badge variant="outline">{String(panelBin.node.extra.os_build)}</Badge> : null}
                    </div>
                  </div>
                  <Button size="icon-sm" variant="ghost" onClick={() => { setPanelBin(null); setSelected(null); }}>
                    <XIcon />
                  </Button>
                </div>
                <Input
                  className="h-8 text-xs"
                  placeholder={`filter ${panelBin.summary.functions.length} functions…`}
                  value={fnFilter}
                  onChange={(e) => setFnFilter(e.target.value)}
                />
                <div className="space-y-1">
                  {filteredFns.map((f) => (
                    <button
                      key={f.name}
                      type="button"
                      className="flex w-full items-center justify-between rounded-md border px-2 py-1.5 text-left font-mono text-xs hover:bg-accent"
                      onClick={() => setChainFrom(`${panelBin.name}!${f.name}`)}
                      title="set as chain start"
                    >
                      <span className="min-w-0 truncate">{f.name}</span>
                      <span className="ml-2 shrink-0 text-muted-foreground">↖{f.in} ↘{f.out}</span>
                    </button>
                  ))}
                  {filteredFns.length === 0 && <div className="py-4 text-center text-xs text-muted-foreground">no match</div>}
                </div>
                <div className="text-[11px] text-muted-foreground">
                  click a function → chain start · {panelBin.summary.edges.length} edges drawn
                </div>
              </div>
            ) : (
              selected && (
                <div className="space-y-3">
                  <div className="flex items-start justify-between gap-2">
                    <div>
                      <div className="text-[11px] text-muted-foreground uppercase tracking-wide">{selected.group}</div>
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
                </div>
              )
            )}
          </div>
        )}
      </div>
    </div>
  );
}