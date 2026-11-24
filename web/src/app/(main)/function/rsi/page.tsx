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
import { Checkbox } from "@/components/ui/checkbox";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { api, ApiError } from "@/lib/api";

const ForceGraph2D = dynamic(() => import("react-force-graph-2d"), { ssr: false });

type WorldRow = { id: number; task_id: number; name: string; created_at: string };
type WorldNode = { id: number; kind: string; parent: number; score: number; cost: number };
type WorldLink = { source: number; target: number };
type WorldDetail = { id: number; task_id: number; name: string; root: number; B: number; S: number; nodes: WorldNode[]; links: WorldLink[] };
// world_ids = the replay worlds a version was evaluated on; empty/absent = all.
type Policy = { version: number; code: string; note: string; origin: string; avg_score: number; world_ids?: number[] };
type DreamResult = {
  dreamer: string;
  best: { code: string; note: string; avg_score: number };
  candidates: { policy: string; avg_score: number }[];
  world_ids?: number[] | null;
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
  // Worlds included in a dream cycle (multi-select). Seeded once with every
  // world — the historical all-worlds behavior — so a deliberate "None" or
  // partial selection survives refreshes of the list.
  const [picked, setPicked] = React.useState<Set<number>>(new Set());
  const pickedSeeded = React.useRef(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const [ws, pol] = await Promise.all([api.rsiWorlds(), api.rsiPolicies()]);
      setWorlds(ws);
      setPolicies(pol.policies ?? []);
      setDeployed(pol.deployed_version ?? 0);
      if (!pickedSeeded.current && ws.length > 0) {
        pickedSeeded.current = true;
        setPicked(new Set(ws.map((w) => w.id)));
      }
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
      setDreamResult(await api.rsiRedream({ world_ids: [...picked] }));
    } catch (e) {
      toast.error(errText(e));
    } finally {
      setDreaming(false);
    }
  }

  async function adopt(res: DreamResult) {
    try {
      await api.rsiAdopt({
        code: res.best.code,
        note: res.best.note,
        avg_score: res.best.avg_score,
        world_ids: res.world_ids ?? undefined,
      });
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
              <span className="ml-auto text-xs font-normal text-muted-foreground">
                {picked.size} in cycle
              </span>
              <Button
                variant="ghost"
                size="sm"
                className="h-6 px-2 text-xs"
                disabled={worlds.length === 0}
                onClick={() => setPicked(new Set(worlds.map((w) => w.id)))}
              >
                All
              </Button>
              <Button
                variant="ghost"
                size="sm"
                className="h-6 px-2 text-xs"
                disabled={worlds.length === 0}
                onClick={() => setPicked(new Set())}
              >
                None
              </Button>
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-1">
            {worlds.length === 0 && (
              <p className="text-sm text-muted-foreground">no worlds yet — archive a task to replay it</p>
            )}
            {worlds.map((w) => {
              const inCycle = picked.has(w.id);
              const viewing = selected === w.id;
              return (
                <div
                  key={w.id}
                  className={`flex items-center gap-1 rounded-md pr-2 text-sm ${viewing ? "bg-primary text-primary-foreground" : "hover:bg-muted"} ${
                    inCycle ? "" : "opacity-60"
                  }`}
                >
                  <label
                    className="flex shrink-0 cursor-pointer items-center gap-1 py-1 pl-1"
                    title={inCycle ? "Included in the dream cycle — uncheck to exclude" : "Excluded from the dream cycle"}
                  >
                    <Checkbox
                      checked={inCycle}
                      onCheckedChange={(v) =>
                        setPicked((prev) => {
                          const next = new Set(prev);
                          if (v) next.add(w.id);
                          else next.delete(w.id);
                          return next;
                        })
                      }
                      aria-label={`${inCycle ? "Exclude" : "Include"} ${w.name} in the dream cycle`}
                    />
                  </label>
                  <button
                    type="button"
                    onClick={() => setSelected(w.id)}
                    className="min-w-0 flex-1 cursor-pointer py-1.5 text-left"
                    title="Click to view this world's graph"
                  >
                    <span className="font-medium">{w.name}</span>
                    <span className={`ml-2 text-xs ${viewing ? "text-primary-foreground/70" : "text-muted-foreground"}`}>
                      task {w.task_id}
                    </span>
                  </button>
                </div>
              );
            })}
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
                  <Badge variant="outline" className="ml-1.5 h-4 px-1.5 font-normal text-[10px]">
                    {p.world_ids && p.world_ids.length > 0
                      ? `${p.world_ids.length} ${p.world_ids.length === 1 ? "world" : "worlds"}`
                      : "all worlds"}
                  </Badge>
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
            <Button size="sm" onClick={() => void dream()} disabled={dreaming || picked.size === 0}>
              {dreaming ? <Loader2Icon className="size-4 animate-spin" /> : <PlayIcon className="size-4" />} Run cycle
            </Button>
          </CardHeader>
          <CardContent>
            {!dreamResult ? (
              <>
                <p className="text-sm text-muted-foreground">
                  One cycle evaluates every candidate policy across the {picked.size} selected{" "}
                  {picked.size === 1 ? "world" : "worlds"}
                  {picked.size !== worlds.length ? ` (of ${worlds.length})` : ""} (replay is free) and the dreamer picks
                  with rationale. Dry run — adoption is a separate click. Tick worlds in the list to scope the policy to
                  specific tasks.
                </p>
                {picked.size === 0 && (
                  <p className="mt-2 text-sm text-amber-600 dark:text-amber-400">
                    No worlds selected — tick at least one.
                  </p>
                )}
              </>
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
                  <p className="mt-1 text-xs text-muted-foreground">
                    scope:{" "}
                    {dreamResult.world_ids && dreamResult.world_ids.length > 0
                      ? `${dreamResult.world_ids.length} ${dreamResult.world_ids.length === 1 ? "world" : "worlds"}`
                      : `all ${worlds.length} worlds`}
                  </p>
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
