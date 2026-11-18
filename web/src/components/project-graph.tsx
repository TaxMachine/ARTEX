"use client";

import * as React from "react";
import dynamic from "next/dynamic";

import { Loader2Icon, RefreshCwIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { api, ApiError } from "@/lib/api";

const ForceGraph2D = dynamic(() => import("react-force-graph-2d"), { ssr: false });

export type GraphNode = {
  id: string;
  group: string;
  label: string;
  sub?: string;
  color?: string;
  extra?: Record<string, unknown>;
  x?: number;
  y?: number;
};

export type GraphEdge = { from: string; to: string; kind: string };

export type ProjectGraph = {
  project: { key: string; name: string; binaries: string[] };
  nodes: GraphNode[];
  edges: GraphEdge[];
  crossmap: { function_nodes: number; function_edges: number };
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

export function ProjectGraphView({ projectKey }: { projectKey: string }) {
  const [graph, setGraph] = React.useState<ProjectGraph | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [selected, setSelected] = React.useState<GraphNode | null>(null);
  const fgRef = React.useRef<{ zoomToFit: (ms?: number, px?: number) => void } | null>(null);

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

  const data = React.useMemo(() => {
    if (!graph) return { nodes: [], links: [] };
    return {
      nodes: graph.nodes,
      links: graph.edges.map((e) => ({ source: e.from, target: e.to, kind: e.kind })),
    };
  }, [graph]);

  const nodeColor = (n: GraphNode) => {
    if (n.group === "finding" && n.color && SEV_COLORS[n.color]) return SEV_COLORS[n.color];
    return GROUP_COLORS[n.group] ?? "#64748b";
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          {(Object.keys(GROUP_COLORS) as (keyof typeof GROUP_COLORS)[]).map((g) => (
            <span key={g} className="flex items-center gap-1 text-xs text-muted-foreground">
              <span className="inline-block size-2.5 rounded-full" style={{ background: GROUP_COLORS[g] }} />
              {g}
            </span>
          ))}
          {graph && graph.crossmap.function_nodes > 0 && (
            <span className="text-xs text-muted-foreground">
              · {graph.crossmap.function_nodes} functions / {graph.crossmap.function_edges} edges indexed
            </span>
          )}
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={() => void load()} disabled={loading}>
            {loading ? <Loader2Icon className="size-3.5 animate-spin" /> : <RefreshCwIcon className="size-3.5" />}
            Refresh
          </Button>
          <Button variant="outline" size="sm" onClick={() => fgRef.current?.zoomToFit(400, 60)}>
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
              nodeLabel={(node) => `${(node as GraphNode).label}${(node as GraphNode).sub ? ` (${(node as GraphNode).sub})` : ""}`}
              nodeColor={(node) => nodeColor(node as GraphNode)}
              nodeRelSize={5}
              linkColor={() => "#475569"}
              onNodeClick={(node) => setSelected(node as GraphNode)}
              cooldownTicks={120}
              height={700}
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