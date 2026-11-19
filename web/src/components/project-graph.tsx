"use client";

import * as React from "react";
import dynamic from "next/dynamic";

import { Loader2Icon, RefreshCwIcon, SearchIcon, XIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api, ApiError, http } from "@/lib/api";

const ForceGraph2D = dynamic(() => import("react-force-graph-2d"), { ssr: false });

type GNode = {
  id: string;
  group: string; // binary | finding
  label: string;
  sub?: string;
  color?: string;
  extra?: Record<string, unknown>;
  x?: number;
  y?: number;
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

// cluster reveal: at scale <= REVEAL_START the binary is a solid ball; at
// scale >= REVEAL_END it has fully dissolved into its function balls.
// Tuned so the dissolve begins just past the fit-view scale (~0.6) and
// completes before the canvas is pixel-peeking.
const REVEAL_START = 0.75;
const REVEAL_END = 1.6;

type ClusterFn = { name: string; in: number; out: number };
type Cluster = { fns: ClusterFn[]; edges: { a: number; b: number }[]; idx: Map<string, number> };

async function crossmap<T = Record<string, unknown>>(path: string): Promise<T> {
  return http<T>(`/crossmap/${path}`);
}

async function fetchCluster(bin: string): Promise<Cluster | null> {
  try {
    const r = await crossmap<{ functions: ClusterFn[]; edges: { from: string; to: string }[] }>(
      `map/expand?binary=${encodeURIComponent(bin)}&limit=60`,
    );
    const fns = r.functions ?? [];
    const idx = new Map<string, number>();
    fns.forEach((f, i) => idx.set(f.name, i));
    const edges: { a: number; b: number }[] = [];
    for (const e of r.edges ?? []) {
      const a = idx.get(e.from.split("!").pop() ?? "");
      const b = idx.get(e.to.split("!").pop() ?? "");
      if (a != null && b != null) edges.push({ a, b });
    }
    return { fns, edges, idx };
  } catch {
    return null;
  }
}

// deterministic spiral position for the i-th of n functions
function spiral(i: number, n: number, R: number): { x: number; y: number } {
  if (n === 1) return { x: 0, y: 0 };
  const r = R * Math.sqrt((i + 0.6) / n);
  const th = i * 2.39996323; // golden angle
  return { x: r * Math.cos(th), y: r * Math.sin(th) };
}

export function ProjectGraphView({ projectKey }: { projectKey: string }) {
  const [graph, setGraph] = React.useState<ProjectGraph | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [detail, setDetail] = React.useState<
    | { kind: "binary"; node: GNode; cluster: Cluster | null }
    | { kind: "finding"; node: GNode }
    | { kind: "code"; binary: string; fn: string }
    | null
  >(null);
  const [fnFilter, setFnFilter] = React.useState("");
  const [showGenerations, setShowGenerations] = React.useState(false);
  const [chainFrom, setChainFrom] = React.useState("");
  const [chainTo, setChainTo] = React.useState("");
  const [chainPath, setChainPath] = React.useState<string[] | null>(null);
  const [code, setCode] = React.useState<string | null>(null);
  const [codeLoading, setCodeLoading] = React.useState(false);
  const fgRef = React.useRef<{ zoomToFit: (ms?: number, px?: number) => void } | null>(null);
  const canvasWrap = React.useRef<HTMLDivElement | null>(null);
  const [dim, setDim] = React.useState({ w: 900, h: 650 });
  const clusters = React.useRef<Map<string, Cluster>>(new Map());
  const clustersFetched = React.useRef<Set<string>>(new Set());
  const [, bumpRepaint] = React.useState(0);

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

  // fetch the function cluster for every binary once the graph loads
  const ensureClusters = React.useCallback(async (g: ProjectGraph | null) => {
    if (!g) return;
    const names = g.nodes
      .filter((n) => n.group === "binary")
      .map((n) => String(n.extra?.name ?? n.label.split(" ")[0]));
    await Promise.all(
      names.map(async (name) => {
        if (clustersFetched.current.has(name)) return;
        clustersFetched.current.add(name);
        const c = await fetchCluster(name);
        if (c) {
          clusters.current.set(name, c);
          bumpRepaint((v) => v + 1);
        }
      }),
    );
  }, []);

  React.useEffect(() => {
    if (graph) void ensureClusters(graph);
  }, [graph, ensureClusters]);

  const openCode = React.useCallback(async (binary: string, fn: string) => {
    setDetail({ kind: "code", binary, fn });
    setCode(null);
    setCodeLoading(true);
    try {
      const r = await crossmap<{ available: boolean; code?: string; error?: string }>(
        `map/decompile?binary=${encodeURIComponent(binary)}&function=${encodeURIComponent(fn)}`,
      );
      if (r.available && r.code) {
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
  }, []);

  const findChain = React.useCallback(async () => {
    if (!chainFrom.includes("!") || !chainTo.includes("!")) {
      toast.error("from/to must each be binary!function");
      return;
    }
    try {
      const r = await crossmap<{ paths: { hops: { ref: string }[] }[] }>(
        `map/paths?from=${encodeURIComponent(chainFrom)}&to=${encodeURIComponent(chainTo)}&max_hops=6`,
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
    const visIds = new Set(
      graph.nodes
        .filter((n) => !(n.group === "binary" && !showGenerations && n.extra?.is_latest === false))
        .map((n) => n.id),
    );
    return {
      nodes: graph.nodes.filter((n) => visIds.has(n.id)),
      links: graph.edges
        .filter((e) => visIds.has(e.from) && visIds.has(e.to))
        .map((e) => ({ source: e.from, target: e.to, kind: e.kind })),
    };
  }, [graph, showGenerations]);

  // finding token match: highlight function balls whose name appears in a
  // finding title of this project (best-effort anchoring)
  const findingTokens = React.useMemo(() => {
    if (!graph) return [] as { sev: string; toks: string[] }[];
    return graph.nodes
      .filter((n) => n.group === "finding")
      .map((n) => ({
        sev: String(n.color ?? "medium"),
        toks: (n.label.match(/[A-Za-z_][A-Za-z0-9_]{5,}/g) ?? []).map((t) => t.toLowerCase()),
      }));
  }, [graph]);

  const hlFor = React.useCallback(
    (fnName: string): string | null => {
      const lower = fnName.toLowerCase();
      for (const f of findingTokens) {
        if (f.toks.some((t) => lower.includes(t) || t.includes(lower))) return f.sev;
      }
      return null;
    },
    [findingTokens],
  );

  const chainSet = React.useMemo(() => new Set(chainPath ?? []), [chainPath]);

  const nodeCanvasObject = React.useCallback(
    (node: GNode, ctx: CanvasRenderingContext2D, scale: number) => {
      const x = node.x ?? 0;
      const y = node.y ?? 0;

      if (node.group === "finding") {
        const sev = node.color ?? "medium";
        const c = SEV_COLORS[sev] ?? "#f87171";
        ctx.beginPath();
        ctx.arc(x, y, 6, 0, 2 * Math.PI);
        ctx.fillStyle = c;
        ctx.fill();
        ctx.beginPath();
        ctx.arc(x, y, 9.5, 0, 2 * Math.PI);
        ctx.strokeStyle = `${c}66`;
        ctx.lineWidth = 2;
        ctx.stroke();
        ctx.font = "4px ui-sans-serif, sans-serif";
        ctx.textAlign = "center";
        ctx.textBaseline = "top";
        ctx.fillStyle = "#fecaca";
        ctx.fillText(node.label.length > 34 ? `${node.label.slice(0, 33)}…` : node.label, x, y + 11);
        return;
      }

      // binary: solid ball at low zoom; dissolves into its function cluster
      const binName = String(node.extra?.name ?? node.label.split(" ")[0]);
      const cluster = clusters.current.get(binName);
      const nFn = cluster?.fns.length ?? Number(node.extra?.functions ?? 0);
      const R = 14 + Math.min(16, Math.sqrt(nFn) * 0.9);
      let t = 0;
      if (scale <= REVEAL_START) t = 0;
      else if (scale >= REVEAL_END) t = 1;
      else t = (scale - REVEAL_START) / (REVEAL_END - REVEAL_START);
      t = t * t * (3 - 2 * t); // smoothstep

      // cluster behind the ball
      if (cluster && t > 0.02) {
        // spread grows with the dissolve so the web stays readable instead of
        // compressing into a disk at full zoom
        const CR = R * (1.1 + 2.6 * t);
        // internal edges
        ctx.lineWidth = 0.5;
        for (const e of cluster.edges) {
          const pa = spiral(e.a, cluster.fns.length, CR);
          const pb = spiral(e.b, cluster.fns.length, CR);
          ctx.strokeStyle = `rgba(120,144,180,${0.35 * t})`;
          ctx.beginPath();
          ctx.moveTo(x + pa.x, y + pa.y);
          ctx.lineTo(x + pb.x, y + pb.y);
          ctx.stroke();
        }
        // function balls
        cluster.fns.forEach((f, i) => {
          const p = spiral(i, cluster.fns.length, CR);
          const deg = f.in + f.out;
          const r = 0.9 + Math.min(1.6, deg / 60);
          const hl = hlFor(f.name);
          const onChain = chainSet.has(`${binName}!${f.name}`);
          ctx.beginPath();
          ctx.arc(x + p.x, y + p.y, r * (0.6 + 0.4 * t), 0, 2 * Math.PI);
          ctx.fillStyle = onChain ? "#22d3ee" : hl ? (SEV_COLORS[hl] ?? "#f87171") : `rgba(148,168,196,${0.85})`;
          ctx.fill();
          if (hl) {
            ctx.beginPath();
            ctx.arc(x + p.x, y + p.y, r + 2.2, 0, 2 * Math.PI);
            ctx.strokeStyle = `${SEV_COLORS[hl]}55`;
            ctx.lineWidth = 1;
            ctx.stroke();
          }
          // labels for the biggest hubs when nearly dissolved
          if (t > 0.6 && deg > 30) {
            ctx.font = "2.6px ui-monospace, monospace";
            ctx.textAlign = "center";
            ctx.textBaseline = "top";
            ctx.fillStyle = "rgba(203,213,225,0.9)";
            ctx.fillText(f.name.length > 22 ? `${f.name.slice(0, 21)}…` : f.name, x + p.x, y + p.y + r + 0.6);
          }
        });
      }

      // the ball itself fades out as the cluster dissolves in
      const ballAlpha = 1 - t * 0.9;
      if (ballAlpha > 0.03) {
        ctx.beginPath();
        ctx.arc(x, y, R, 0, 2 * Math.PI);
        ctx.fillStyle = `rgba(56,189,248,${ballAlpha})`;
        ctx.fill();
        ctx.strokeStyle = `rgba(56,189,248,${0.5 * ballAlpha})`;
        ctx.lineWidth = 1.5;
        ctx.stroke();
      }

      // label + stats fade with the ball
      const labelAlpha = 1 - t * 0.85;
      if (labelAlpha > 0.08) {
        ctx.textAlign = "center";
        ctx.font = `4.4px ui-sans-serif, sans-serif`;
        ctx.fillStyle = `rgba(232,238,247,${labelAlpha})`;
        ctx.textBaseline = "top";
        const title = node.label.length > 26 ? `${node.label.slice(0, 25)}…` : node.label;
        ctx.fillText(title, x, y + R + 2);
        const fcount = node.extra?.findings as number | undefined;
        const gen = node.extra?.version ? `v${String(node.extra.version).split(".").pop()}` : "";
        const stat = [nFn ? `${nFn.toLocaleString()} fns` : "", gen, fcount ? `${fcount} finding${fcount > 1 ? "s" : ""}` : ""]
          .filter(Boolean)
          .join(" · ");
        if (stat) {
          ctx.font = "3px ui-sans-serif, sans-serif";
          ctx.fillStyle = `rgba(148,163,184,${labelAlpha})`;
          ctx.fillText(stat, x, y + R + 7.5);
        }
      }
    },
    [chainSet, hlFor],
  );

  const nodeArea = React.useCallback((node: GNode, color: string, ctx: CanvasRenderingContext2D) => {
    const R = node.group === "finding" ? 12 : 30;
    ctx.beginPath();
    ctx.arc(node.x ?? 0, node.y ?? 0, R, 0, 2 * Math.PI);
    ctx.fillStyle = color;
    ctx.fill();
  }, []);

  const filteredFns = React.useMemo(() => {
    if (detail?.kind !== "binary" || !detail.cluster) return [];
    const q = fnFilter.trim().toLowerCase();
    const fns = detail.cluster.fns;
    return q ? fns.filter((f) => f.name.toLowerCase().includes(q)) : fns;
  }, [detail, fnFilter]);

  const olderGens = graph ? graph.nodes.filter((n) => n.group === "binary" && n.extra?.is_latest === false) : [];

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <span className="flex items-center gap-1">
            <span className="inline-block size-2.5 rounded-full" style={{ background: "#38bdf8" }} /> binary
          </span>
          <span>zoom in → functions</span>
          {(["critical", "high", "medium", "low"] as const).map((s) => (
            <span key={s} className="flex items-center gap-1">
              <span className="inline-block size-2.5 rounded-full" style={{ background: SEV_COLORS[s] }} /> {s}
            </span>
          ))}
          {graph && (graph.crossmap.function_total ?? 0) > 0 && (
            <span>· {graph.crossmap.function_total?.toLocaleString()} fns indexed</span>
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
            nodePointerAreaPaint={(n, color, ctx) => nodeArea(n as GNode, color, ctx)}
            onNodeClick={(n) => {
              const node = n as GNode;
              if (node.group === "binary") {
                const bin = String(node.extra?.name ?? node.label.split(" ")[0]);
                setFnFilter("");
                setDetail({ kind: "binary", node, cluster: clusters.current.get(bin) ?? null });
              } else {
                setDetail({ kind: "finding", node });
              }
            }}
            cooldownTicks={200}
            minZoom={0.4}
            maxZoom={12}
          />
        ) : (
          <div className="flex h-full items-center justify-center text-muted-foreground">
            <Loader2Icon className="mr-2 size-5 animate-spin" /> loading graph…
          </div>
        )}

        {detail && (
          <div className="absolute right-2 top-2 bottom-2 flex w-[30rem] flex-col gap-2 overflow-hidden rounded-xl border bg-background/95 p-4 text-sm shadow-lg backdrop-blur">
            {detail.kind === "binary" && (
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
                  placeholder={`filter ${detail.cluster?.fns.length ?? 0} functions…`}
                  value={fnFilter}
                  onChange={(e) => setFnFilter(e.target.value)}
                />
                <div className="min-h-0 flex-1 space-y-1 overflow-y-auto pr-1">
                  {(detail.cluster?.fns ?? []).length === 0 && (
                    <div className="py-4 text-center text-xs text-muted-foreground">cluster not loaded yet</div>
                  )}
                  {filteredFns.map((f) => {
                    const hl = hlFor(f.name);
                    return (
                      <button
                        key={f.name}
                        type="button"
                        className="flex w-full items-center justify-between rounded-md border px-2 py-1.5 text-left font-mono text-xs hover:bg-accent"
                        onClick={() => {
                          const bin = String(detail.node.extra?.name);
                          setChainFrom(`${bin}!${f.name}`);
                          void openCode(bin, f.name);
                        }}
                        title="open code + set chain start"
                        style={hl ? { borderColor: SEV_COLORS[hl] ?? "#f87171" } : undefined}
                      >
                        <span className="min-w-0 truncate">
                          {hl && <span className="mr-1 inline-block size-2 rounded-full align-middle" style={{ background: SEV_COLORS[hl] }} />}
                          {f.name}
                        </span>
                        <span className="ml-2 shrink-0 text-muted-foreground">↖{f.in} ↘{f.out}</span>
                      </button>
                    );
                  })}
                </div>
                <div className="text-[11px] text-muted-foreground">click a function → its decompiled code + chain start</div>
              </>
            )}
            {detail.kind === "code" && (
              <>
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <div className="text-[11px] text-muted-foreground uppercase tracking-wide">function · {detail.binary}</div>
                    <div className="truncate font-mono text-sm font-semibold">{detail.fn}</div>
                  </div>
                  <Button size="icon-sm" variant="ghost" onClick={() => setDetail(null)}>
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
                  <Button size="sm" variant="outline" onClick={() => setChainFrom(`${detail.binary}!${detail.fn}`)}>
                    set as chain start
                  </Button>
                  <Button size="sm" variant="outline" onClick={() => setChainTo(`${detail.binary}!${detail.fn}`)}>
                    set as chain end
                  </Button>
                </div>
              </>
            )}
            {detail.kind === "finding" && (
              <>
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <div className="text-[11px] text-muted-foreground uppercase tracking-wide">
                      finding · {String(detail.node.color)} · {String(detail.node.extra?.status)}
                    </div>
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