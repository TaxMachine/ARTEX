"use client";

import * as React from "react";

import dynamic from "next/dynamic";
import {
  BrainCircuitIcon,
  CheckCircle2Icon,
  FlaskConicalIcon,
  Loader2Icon,
  MoonIcon,
  NetworkIcon,
  PlayIcon,
  RefreshCwIcon,
  SparklesIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { api, ApiError } from "@/lib/api";

const ForceGraph2D = dynamic(() => import("react-force-graph-2d"), { ssr: false });

type WorldRow = { id: number; task_id: number; name: string; created_at: string };
type WorldNode = { id: number; kind: string; parent: number; score: number; cost: number };
type WorldLink = { source: number; target: number };
type WorldDetail = { id: number; task_id: number; name: string; root: number; B: number; S: number; nodes: WorldNode[]; links: WorldLink[] };
type Policy = { version: number; code: string; note: string; origin: string; avg_score: number };
type DreamResult = {
  dreamer: string;
  best: { code: string; note: string; avg_score: number };
  candidates: { policy: string; avg_score: number }[];
  note?: string;
};

const kindColor: Record<string, string> = {
  goal: "#8b5cf6",
  intent: "#3b82f6",
  fact: "#10b981",
  finding: "#ef4444",
  hint: "#f59e0b",
  digest: "#94a3b8",
};

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
}

export default function RsiPage() {
  const [worlds, setWorlds] = React.useState<WorldRow[]>([]);
  const [selected, setSelected] = React.useState<number | null>(null);
  const [detail, setDetail] = React.useState<WorldDetail | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [detailLoading, setDetailLoading] = React.useState(false);
  const [policies, setPolicies] = React.useState<Policy[]>([]);
  const [deployed, setDeployed] = React.useState(0);
  const [dreaming, setDreaming] = React.useState(false);
  const [dreamResult, setDreamResult] = React.useState<DreamResult | null>(null);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const [ws, pol] = await Promise.all([api.rsiWorlds(), api.rsiPolicies()]);
      setWorlds(ws);
      setPolicies(pol.policies ?? []);
      setDeployed(pol.deployed_version ?? 0);
      if (!selected && ws.length > 0) setSelected(ws[0].id);
    } catch (e) {
      toast.error(errText(e));
    } finally {
      setLoading(false);
    }
  }, [selected]);

  React.useEffect(() => {
    void load();
  }, [load]);

  React.useEffect(() => {
    if (selected == null) return;
    setDetailLoading(true);
    api
      .rsiWorldDetail(selected)
      .then(setDetail)
      .catch((e) => toast.error(errText(e)))
      .finally(() => setDetailLoading(false));
  }, [selected]);

  async function dream() {
    setDreaming(true);
    setDreamResult(null);
    try {
      setDreamResult(await api.rsiRedream({}));
    } catch (e) {
      toast.error(errText(e));
    } finally {
      setDreaming(false);
    }
  }

  async function adopt(res: DreamResult) {
    try {
      await api.rsiAdopt({ code: res.best.code, note: res.best.note, avg_score: res.best.avg_score });
      toast.success(`policy v${deployed + 1} adopted: ${res.best.code}`);
      setDreamResult(null);
      await load();
    } catch (e) {
      toast.error(errText(e));
    }
  }

  const graphData = React.useMemo(() => {
    if (!detail) return null;
    return {
      nodes: detail.nodes.map((n) => ({ id: n.id, kind: n.kind, score: n.score, color: kindColor[n.kind] ?? "#64748b" })),
      links: detail.links,
    };
  }, [detail]);

  const kindCounts = React.useMemo(() => {
    const m: Record<string, number> = {};
    detail?.nodes.forEach((n) => {
      m[n.kind] = (m[n.kind] ?? 0) + 1;
    });
    return m;
  }, [detail]);

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 text-xl font-semibold">
            <BrainCircuitIcon className="size-5" /> Dream-RSI
          </h1>
          <p className="text-sm text-muted-foreground">
            Replay worlds from completed task history, the exploration-policy ranking, and one-click dreaming cycles.
          </p>
        </div>
        <Button variant="outline" onClick={() => void load()} disabled={loading}>
          {loading ? <Loader2Icon className="size-4 animate-spin" /> : <RefreshCwIcon className="size-4" />} Refresh
        </Button>
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        {/* worlds list */}
        <Card className="lg:col-span-1">
          <CardHeader className="pb-2">
            <CardTitle className="flex items-center gap-2 text-base">
              <NetworkIcon className="size-4" /> Worlds ({worlds.length})
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-1">
            {worlds.map((w) => (
              <button
                key={w.id}
                type="button"
                onClick={() => setSelected(w.id)}
                className={`w-full cursor-pointer rounded-md px-2 py-1.5 text-left text-sm ${
                  selected === w.id ? "bg-primary text-primary-foreground" : "hover:bg-muted"
                }`}
              >
                <span className="font-medium">{w.name}</span>
                <span className={`ml-2 text-xs ${selected === w.id ? "text-primary-foreground/70" : "text-muted-foreground"}`}>
                  task {w.task_id}
                </span>
              </button>
            ))}
          </CardContent>
        </Card>

        {/* world graph */}
        <Card className="lg:col-span-2">
          <CardHeader className="pb-2">
            <CardTitle className="text-base">
              {detail ? detail.name : "World"}{" "}
              {detail && (
                <span className="ml-2 text-xs font-normal text-muted-foreground">
                  {detail.B} nodes · best score {detail.S.toFixed(2)}
                </span>
              )}
            </CardTitle>
          </CardHeader>
          <CardContent>
            {detailLoading && !detail ? (
              <div className="flex h-72 items-center justify-center text-sm text-muted-foreground">
                <Loader2Icon className="mr-2 size-4 animate-spin" /> loading tree…
              </div>
            ) : graphData && graphData.nodes.length > 0 ? (
              <div className="h-72 overflow-hidden rounded-md border">
                <ForceGraph2D
                  graphData={graphData as never}
                  nodeLabel={(n: object) => { const o = n as { kind?: string; score?: number }; return `${o.kind ?? ""} · score ${(o.score ?? 0).toFixed(2)}`; }}
                  nodeColor={(n: object) => (n as { color?: string }).color ?? "#64748b"}
                  nodeVal={(n: object) => 1 + ((n as { score?: number }).score ?? 0) * 5}
                  linkDirectionalParticles={2}
                  height={288}
                  cooldownTicks={120}
                />
              </div>
            ) : (
              <div className="flex h-72 items-center justify-center text-sm text-muted-foreground">no nodes</div>
            )}
            {Object.keys(kindCounts).length > 0 && (
              <div className="mt-2 flex flex-wrap gap-2">
                {Object.entries(kindCounts).map(([k, n]) => (
                  <span key={k} className="flex items-center gap-1 text-xs text-muted-foreground">
                    <span className="inline-block size-2 rounded-full" style={{ background: kindColor[k] ?? "#64748b" }} />
                    {k} · {n}
                  </span>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        {/* policies */}
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="flex items-center gap-2 text-base">
              <FlaskConicalIcon className="size-4" /> Policies
              {deployed > 0 && (
                <Badge variant="secondary" className="ml-1">
                  deployed: v{deployed}
                </Badge>
              )}
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-1">
            {policies.length === 0 && <p className="text-sm text-muted-foreground">none yet — run a dream cycle</p>}
            {policies.map((p) => (
              <div
                key={p.version}
                className={`flex items-center justify-between gap-2 rounded-md px-2 py-1.5 text-sm ${
                  p.version === deployed ? "border bg-muted/50" : ""
                }`}
              >
                <div className="min-w-0">
                  <span className="font-mono text-xs text-muted-foreground">v{p.version}</span>{" "}
                  <span className="font-medium">{p.code}</span>
                  <div className="truncate text-xs text-muted-foreground" title={p.note}>
                    {p.note}
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <span className="font-mono text-xs">V={p.avg_score?.toFixed(4) ?? "—"}</span>
                  {p.version === deployed && <CheckCircle2Icon className="size-4 text-emerald-600" />}
                </div>
              </div>
            ))}
          </CardContent>
        </Card>

        {/* dreaming */}
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="flex items-center gap-2 text-base">
              <MoonIcon className="size-4" /> Dreaming
            </CardTitle>
            <Button size="sm" onClick={() => void dream()} disabled={dreaming || worlds.length === 0}>
              {dreaming ? <Loader2Icon className="size-4 animate-spin" /> : <PlayIcon className="size-4" />} Run cycle
            </Button>
          </CardHeader>
          <CardContent>
            {!dreamResult ? (
              <p className="text-sm text-muted-foreground">
                One cycle evaluates every candidate policy across all {worlds.length} worlds (replay is free) and the
                GLM dreamer picks with rationale. Dry run — adoption is a separate click.
              </p>
            ) : (
              <div className="space-y-3">
                <div>
                  <div className="flex items-center gap-2 text-sm font-medium">
                    <SparklesIcon className="size-4 text-amber-500" />
                    best: <span className="font-mono">{dreamResult.best.code}</span>
                    <span className="font-mono text-xs text-muted-foreground">V={dreamResult.best.avg_score.toFixed(4)}</span>
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">{dreamResult.best.note}</p>
                  <p className="mt-1 text-xs text-muted-foreground">dreamer: {dreamResult.dreamer}</p>
                </div>
                <div className="space-y-0.5">
                  {dreamResult.candidates
                    .slice()
                    .sort((a, b) => b.avg_score - a.avg_score)
                    .slice(0, 5)
                    .map((c) => (
                      <div key={c.policy} className="flex justify-between text-xs">
                        <span className="font-mono">{c.policy}</span>
                        <span className="font-mono text-muted-foreground">{c.avg_score.toFixed(4)}</span>
                      </div>
                    ))}
                </div>
                <Button size="sm" onClick={() => void adopt(dreamResult)}>
                  <CheckCircle2Icon className="size-4" /> Adopt as v{deployed + 1}
                </Button>
              </div>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
