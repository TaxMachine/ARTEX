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
  group: string; // binary | finding | function
  label: string;
  sub?: string;
  color?: string;
  extra?: Record<string, unknown>;
  x?: number;
  y?: number;
  hl?: boolean;
  findingId?: number;
};

type GLink = { source: string | GNode; target: string | GNode; kind: string; hl?: boolean };

export type ProjectGraph = {
  project: { key: string; name: string; binaries: string[] };
  nodes: GNode[];
  edges: { from: string; to: string; kind: string }[];
  crossmap: { function_total?: number; binaries_indexed?: number };
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

// threshold (global scale) at which function balls render
const FN_ZOOM = 1.4;

export function ProjectGraphView({ projectKey }: { projectKey: string }) {
  const [graph, setGraph] = React.useState<ProjectGraph | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [detail, setDetail] = React.useState<
    | { kind: "binary"; node: GNode; summary: ExpandedBin }
    | { kind: "function"; node: GNode }
    | { kind: "finding"; node: GNode }
    | null
  >(null);
  const [fnFilter, setFnFilter] = React.useState("");
  const [showGenerations, setShowGenerations] = React.useState(false);
  const [chainFrom, setChainFrom] = React.useState("");
  const [chainTo, setChainTo] = React.useState("");
  const [chainPath, setChainPath] = React.useState<string[] | null>(null);
  const [code, setCode] = React.useState<string | null>(null);
  const [codeLoading, setCodeLoading] = React.useState(false);
  const fgRef = React.useRef<{ zoomToFit: (ms?: number, px?: number) => void; zoom: (z: number, ms?: number) => void } | null>(null);
  const expandedRef = React.useRef<Set<string>>(new Set());
  const canvasWrap = React.useRef<HTMLDivElement | null>(null);
  const [dim, setDim] = React.useState({ w: 900, h: 650 });
  const zoomRef = React.useRef(1);

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

  // finding titles → candidate function-name matches (for glow attach)
  const findingFnMatches = React.useMemo(() => {
    if (!graph) return new Map<string, GNode>();
    const m = new Map<string, GNode>();
    for (const n of graph.nodes) {
      if (n.group !== "finding") continue;
      // crude: finding label often mentions the API; take quoted/backticked tokens
      const toks = n.label.match(/[A-Za-z_][A-Za-z0-9_]{5,}/g) ?? [];
      m.set(n.id, n);
      (n as GNode & { tokens?: string[] }).tokens = toks;
    }
    return m;
  }, [graph]);

  const expandBinary = React.useCallback(
    async (binName: string) => {
      if (expandedRef.current.has(binName)) return null;
      expandedRef.current.add(binName);
      try {
        const sum = await crossmap<ExpandedBin>(`/map/expand?binary=${encodeURIComponent(binName)}&limit=40`);
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
        return sum;
      } catch (e) {
        expandedRef.current.delete(binName);
        toast.error(`expand failed: ${String(e)}`);
        return null;
      }
    },
    [],
  );

  const openBinary = React.useCallback(
    async (node: GNode) => {
      const binName = String(node.extra?.name ?? node.label.split(" ")[0]);
      setFnFilter("");
      setDetail({ kind: "binary", node, summary: { functions: [], edges: [] } });
      const sum = await expandBinary(binName);
      if (sum) setDetail({ kind: "binary", node, summary: sum });
    },
    [expandBinary],
  );

  const openFunction = React.useCallback(
    async (node: GNode) => {
      const bin = String(node.extra?.binary ?? node.extra?.name ?? "");
      const fn = node.label;
      setDetail({ kind: "function", node });
      setCode(null);
      setCodeLoading(true);
      try {
        const r = await crossmap<{ available: boolean; code?: string; error?: string }>(
          `/map/decompile?binary=${encodeURIComponent(bin)}&function=${encodeURIComponent(fn)}`,
        );
        if (r.available && r.code) {
          // the passthrough wraps JSON; try to pull pseudocode out
          let text = r.code;
          try {
            const inner = JSON.parse(r.code) as { pseudocode?: string };
            if (inner.pseudocode) text = inner.pseudocode;
          } catch {
            /* raw text */
          }
          setCode(text);
        } else {
          setCode(r.error ?? "decompile unavailable");
        }
      } catch (e) {
        setCode(`decompile failed: ${String(e)}`);
      } finally {
        setCodeLoading(false);
      }
    },
    [],
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
      toast.success(`${r.paths[0].hops.length}-hop chain highlighted`);
    } catch (e) {
      toast.error(`chain search failed: ${String(e)}`);
    }
  }, [chainFrom, chainTo]);

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
    if (n.group === "binary") return "#38bdf8";
    return "#8fa3bf";
  };

  const nodeSize = (n: GNode) => {
    if (n.group === "binary") return 10 + Math.min(10, ((n.extra?.functions as number) ?? 0) / 1500);
    if (n.group === "finding") return 7;
    return n.hl ? 6 : 3.5;
  };

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
      if (node.group === "finding") {
        // findings glow
        ctx.beginPath();
        ctx.arc(node.x ?? 0, node.y ?? 0, r + 3, 0, 2 * Math.PI);
        ctx.strokeStyle = nodeColor(node) + "88";
        ctx.lineWidth = 1.5;
        ctx.stroke();
      }
      const showLabel = node.group !== "function" || globalScale > FN_ZOOM;
      if (showLabel) {
        const x = node.x ?? 0;
        const y = node.y ?? 0;
        ctx.textAlign = "center";
        const title = node.label.length > 26 ? `${node.label.slice(0, 25)}…` : node.label;
        ctx.font = `${node.group === "function" ? 3 : 4.2}px ui-sans-serif, sans-serif`;
        ctx.fillStyle = node.group === "finding" ? "#fecaca" : "rgba(232,238,247,0.95)";
        ctx.textBaseline = "top";
        ctx.fillText(title, x, y + r + 1.5);
        if (node.group === "binary") {
          const f = node.extra?.functions as number | undefined;
          const fcount = node.extra?.findings as number | undefined;
          const gen = node.extra?.version ? `v${String(node.extra.version).split(".").pop()}` : "";
          const stat = [f != null ? `${f.toLocaleString()} fns` : "", gen, fcount ? `${fcount} finding${fcount > 1 ? "s" : ""}` : ""]
            .filter(Boolean)
            .join(" · ");
          if (stat) {
            ctx.font = "3px ui-sans-serif, sans-serif";
            ctx.fillStyle = "rgba(148,163,184,0.95)";
            ctx.fillText(stat, x, y + r + 6.5);
          }
        }
      }
    },
    [chainPath],
  );

  const filteredFns = React.useMemo(() => {
    if (detail?.kind !== "binary") return [];
    const q = fnFilter.trim().toLowerCase();
    if (!q) return detail.summary.functions;
    return detail.summary.functions.filter((f) => f.name.toLowerCase().includes(q));
  }, [detail, fnFilter]);

  const olderGens = graph ? graph.nodes.filter((n) => n.group === "binary" && n.extra?.is_latest === false) : [];

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <span className="flex items-center gap-1">
            <span className="inline-block size-2.5 rounded-full" style={{ background: "#38bdf8" }} /> binary
          </span>
          {(["critical", "high", "medium", "low"] as const).map((s) => (
            <span key={s} className="flex items-center gap-1">
              <span className="inline-block size-2.5 rounded-full" style={{ background: SEV_COLORS[s] }} /> {s} finding
            </span>
          ))}
          {graph && (graph.crossmap.function_total ?? 0) > 0 && (
            <span>· {graph.crossmap.function_total?.toLocaleString()} fns indexed · zoom into a binary to see functions · click a function for its code</span>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {olderGens.length > 0 && (
            <Button size="sm" variant="outline" onClick={() => setShowGenerations((v) => !v)}>
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

      <div ref={canvasWrap} className="relative min-h-0 flex-1 overflow-hidden rounded-xl border bg-card">
        {!loading && graph ? (
          <ForceGraph2D
            ref={fgRef as never}
            graphData={data as never}
            width={dim.w}
            height={dim.h}
            onEngineStop={() => fgRef.current?.zoomToFit(300, 60)}
            nodeCanvasObject={(n, ctx, scale) => nodeCanvasObject(n as GNode, ctx, scale)}
            nodePointerAreaPaint={(n, color, ctx) => {
              const node = n as GNode;
              ctx.beginPath();
              ctx.arc(node.x ?? 0, node.y ?? 0, nodeSize(node) + 4, 0, 2 * Math.PI);
              ctx.fillStyle = color;
              ctx.fill();
            }}
            onNodeClick={(n) => {
              const node = n as GNode;
              if (node.group === "binary") void openBinary(node);
              else if (node.group === "function") void openFunction(node);
              else setDetail({ kind: "finding", node });
            }}
            onZoom={(z) => (zoomRef.current = typeof z === "number" ? z : z.k)}
            cooldownTicks={200}
          />
        ) : (
          <div className="flex h-full items-center justify-center text-muted-foreground">
            <Loader2Icon className="mr-2 size-5 animate-spin" /> loading graph…
          </div>
        )}

        {(detail || codeLoading) && (
          <div className="absolute right-2 top-2 bottom-2 flex w-[30rem] flex-col gap-2 overflow-hidden rounded-xl border bg-background/95 p-4 text-sm shadow-lg backdrop-blur">
            {detail?.kind === "binary" && (
              <>
                <div className="flex items-start justify-between gap-2">
                  <div>
                    <div className="text-[11px] text-muted-foreground uppercase tracking-wide">binary</div>
                    <div className="font-mono text-sm font-semibold">{String(detail.node.extra?.name ?? detail.node.label)}</div>
                    <div className="mt-1 flex flex-wrap gap-1">
                      {detail.node.extra?.functions != null && (
                        <Badge variant="secondary">{Number(detail.node.extra.functions).toLocaleString()} fns</Badge>
                      )}
                      {detail.node.extra?.version ? <Badge variant="outline">{String(detail.node.extra.version)}</Badge> : null}
                    </div>
                  </div>
                  <Button size="icon-sm" variant="ghost" onClick={() => setDetail(null)}>
                    <XIcon />
                  </Button>
                </div>
                <Input
                  className="h-8 text-xs"
                  placeholder={`filter ${detail.summary.functions.length} functions…`}
                  value={fnFilter}
                  onChange={(e) => setFnFilter(e.target.value)}
                />
                <div className="min-h-0 flex-1 space-y-1 overflow-y-auto pr-1">
                  {filteredFns.map((f) => (
                    <button
                      key={f.name}
                      type="button"
                      className="flex w-full items-center justify-between rounded-md border px-2 py-1.5 text-left font-mono text-xs hover:bg-accent"
                      onClick={() => {
                        setChainFrom(`${String(detail.node.extra?.name)}!${f.name}`);
                        void openFunction({ id: `fn:${String(detail.node.extra?.name)}!${f.name}`, group: "function", label: f.name, extra: { binary: String(detail.node.extra?.name) } });
                      }}
                      title="open code + set chain start"
                    >
                      <span className="min-w-0 truncate">{f.name}</span>
                      <span className="ml-2 shrink-0 text-muted-foreground">↖{f.in} ↘{f.out}</span>
                    </button>
                  ))}
                  {filteredFns.length === 0 && <div className="py-4 text-center text-xs text-muted-foreground">no match</div>}
                </div>
              </>
            )}
            {detail?.kind === "function" && (
              <>
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <div className="text-[11px] text-muted-foreground uppercase tracking-wide">function · {String(detail.node.extra?.binary ?? "")}</div>
                    <div className="truncate font-mono text-sm font-semibold">{detail.node.label}</div>
                  </div>
                  <Button size="icon-sm" variant="ghost" onClick={() => { setDetail(null); setCode(null); }}>
                    <XIcon />
                  </Button>
                </div>
                <div className="min-h-0 flex-1 overflow-auto rounded-lg border bg-muted/50 p-2">
                  {codeLoading ? (
                    <div className="flex items-center gap-2 text-xs text-muted-foreground">
                      <Loader2Icon className="size-3.5 animate-spin" /> decompiling…
                    </div>
                  ) : (
                    <pre className="whitespace-pre-wrap break-all font-mono text-[11px] leading-relaxed">{code ?? "no code"}</pre>
                  )}
                </div>
                <div className="flex gap-2">
                  <Button size="sm" variant="outline" onClick={() => setChainFrom(`${String(detail.node.extra?.binary)}!${detail.node.label}`)}>
                    set as chain start
                  </Button>
                  <Button size="sm" variant="outline" onClick={() => setChainTo(`${String(detail.node.extra?.binary)}!${detail.node.label}`)}>
                    set as chain end
                  </Button>
                </div>
              </>
            )}
            {detail?.kind === "finding" && (
              <>
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <div className="text-[11px] text-muted-foreground uppercase tracking-wide">finding · {String(detail.node.color)} · {String(detail.node.extra?.status)}</div>
                    <div className="font-medium leading-snug">{detail.node.label}</div>
                  </div>
                  <Button size="icon-sm" variant="ghost" onClick={() => setDetail(null)}>
                    <XIcon />
                  </Button>
                </div>
                {detail.node.extra?.finding_id != null && (
                  <Button size="sm" variant="outline" asChild>
                    <a href={`/function/findings/detail?id=${String(detail.node.extra.finding_id)}`}>Open finding</a>
                  </Button>
                )}
              </>
            )}
          </div>
        )}
      </div>
    </div>
  );
}