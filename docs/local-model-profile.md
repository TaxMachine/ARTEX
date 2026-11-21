# Local-model worker profile (e.g. Qwen 27B, 262K window)

Hosted frontier models tolerate 100K+ token worker transcripts. A locally
hosted ~27B model does not: KV-cache prefill is the wall-clock cost, and
instruction fidelity degrades well before the hard window. This profile keeps
worker contexts lean using three env levers — all **off unless set**, so
API-model behavior is unchanged.

## Measured baseline (workers on glm-5.3, tasks 9-11)

| metric | value |
|---|---|
| avg input / worker call | 71,398 tokens |
| p-max input | 278,666 tokens |
| calls over 100K | 349 in 2 days |
| system prompt (static+dynamic) | ~12.5 KB (~3K tokens) |
| tool schemas (71 tools) | ~38 KB (~9.5K tokens) |
| single tool output cap | 30,000 chars (spill files keep the rest) |

The transcript — accumulated tool outputs — is the driver, not the system
prompt or schemas. But schemas are paid on *every* call, and compaction only
fires near the window edge (200K assumed), which is far too late for a local
27B.

## Levers (env, additive, all optional)

```bash
# 1. Per-tool output cap (chars). Default 30000. Spill files keep full output
#    on disk; the model sees head + pointer and reads slices on demand.
ARTEX_TOOL_OUT_CHARS=8000

# 2. Compaction window (K tokens) — summarizer runs EARLY. Must be well under
#    the model's real window; 64-96K works for a 262K-window local model.
ARTEX_COMPACTION_K=72

# 3. Tool-RAG: project the worker's tool list to the ~12 most relevant per
#    intent (BM25 over intent text vs tool names+descriptions). Off by default.
ARTEX_TOOL_RAG=1

# 4. Schema slimming: cap each tool description (chars). MEASURED: only 2/71
#    tools exceed 1200 chars (saves ~5%) — descriptions are already tight
#    (~490 chars avg). Tool-RAG (lever 3) is the schema lever that matters.
ARTEX_TOOL_DESC_MAX=1200

# 5. Tool-RAG escape hatch: tools the projection must never hide.
ARTEX_WORKER_GLOBAL_TOOLS=kn_search,record_fact,report_finding
```

## Recommended Qwen-27B stack

```bash
ARTEX_TOOL_OUT_CHARS=8000
ARTEX_COMPACTION_K=72
ARTEX_TOOL_RAG=1
ARTEX_WORKER_GLOBAL_TOOLS=kn_search,record_fact,report_finding,ssh_exec,ps_script,heap_query,heap_spray,fuzz_campaign
```

(`ARTEX_TOOL_DESC_MAX` optional — measured savings ~5%; leave off unless you
grow the catalog with tutorial-style descriptions.)

Expected effect vs baseline: per-call input drops from ~71K toward the
compaction ceiling (~72K worst case, typically far less), tool schemas from
~9.5K to ~1.5-2K tokens (tool-RAG keeps ~12 of 71 tools per intent), and
transcript growth is bounded by the summarizer instead of the window edge. Planner (`plannerToolQuery`) and workers
(`nodeSummary(intent)`) already feed tool-RAG their respective queries.

## Where the levers live

- `agent/localmodel.go` — env parsing + floors, all unit-tested
- `agent/worker.go` — `MaxToolOutputChars` + compaction window override
- `agent/assembly.go` — `slimToolSchemas` (capped-description wrapper)
- `agent/toolrag.go` — keep-list extension from `ARTEX_WORKER_GLOBAL_TOOLS`

## Things already efficient by design

- System prompt is split static/dynamic (prompt-cache friendly; sibling workers
  share the static prefix) — `workerSystemParts`.
- Oversized tool output spills to `cmd-output/` files with head + pointer.
- Compaction (minor/major) already exists for the planner graph.
- `get_worker_output` returns distilled situational summaries, not raw traces.

## Watch-outs

- Don't set `ARTEX_COMPACTION_K` below ~32: the summarizer needs room for the
  intent + recent turns + write-back tools during wrap-up.
- `ARTEX_TOOL_DESC_MAX` below ~600 starts cutting actual instructions (not just
  tutorials) — 1200 is the sweet spot for our catalog.
- Tool-RAG hides tools lexically; if a worker needs an unusual tool it can
  still ask (SearchExtraTools) or the keep-list pins it.
