---
name: fuzzing-triage
description: "Use to FIND memory-corruption bugs in Office-class parsers: run structure-aware mutation campaigns (fuzz_campaign) against Word/Excel, then triage unique crashes — page_heap + windbg crash_triage identifies the primitive, which becomes a fact the planner dispatches on. Covers deploy/seeds/campaign/poll, crash triage, and when to stop fuzzing and start grooming."
mcps: windbg
---

# Fuzzing & crash triage: the find side

`fuzz_campaign` is the bug-finder for document parsers; hand-crafting malformed
files is the fallback, not the default. Division of labor: **the fuzzer finds
crashes, you triage them into primitives, the heap-grooming skill turns
primitives into chains.**

## 1. Setup (once per machine)

```
fuzz_campaign(machine="office-vm", action="deploy")        # pushes the engine
# seeds: a handful of small VALID docs exercising the features you want hit
#   (styles, tables, RTD, shared strings, embedded objects...). Build them
#   with COM (Workbooks.Add -> SaveAs) or ssh_upload existing ones into
#   C:\fuzz\seeds. 3-10 seeds of <100KB each is the sweet spot.
```

## 2. Campaign

```
fuzz_campaign(machine="office-vm", action="start", target="EXCEL", seconds=3600, mode="all")
# detached — do other work, poll:
fuzz_campaign(machine="office-vm", action="status")
fuzz_campaign(machine="office-vm", action="crashes")   # unique signatures + files
```

- Modes: `flip` (byte flips), `chunk` (structure-block splice/dup/cut), `xml`
  (OOXML-aware: mutates val= attributes, tags, ids, numbers — including
  re-entering the zip to hit a random part). `all` mixes them.
- Throughput: ~one open per 5-15s (COM open+close dominates). An hour ≈
  250-700 executions; long campaigns (up to 6h) pay off — run them in the
  background while other intents work.
- More seeds hitting more features > more raw iterations of one seed.

## 3. Triage each unique crash

```
fuzz_campaign(..., action="crashes")          # -> signature + crashing file path
# stage the crashing input, instrument, and reproduce under the debugger:
page_heap(machine="office-vm", process="EXCEL.EXE", action="enable", mode="full")
# open the crashing file under windbg (launch or attach) — crash_triage names
# the faulting instruction/module; map WINWORD/EXCEL+off to a function via
# crossmap when indexed. Record the primitive:
record_fact(intent_id=..., summary="heap overflow primitive in <function> via <structure>",
            confidence="observed",
            detail="crash sig <sig>; faulting <module>+<off>; input staged at <path>; repro 3/3")
```

- A crash is a **primitive** (fact), NOT a finding — per the confirmation
  standard. The finding is the chain it composes into.
- Dedupe is by WER signature (P1-P7 tuple): same signature = same bug, skip.
- If the crash survives WITHOUT page_heap: even better — record it as
  triggerable under default settings.

## 4. Loop discipline

- Fuzz → triage → record primitive → (planner dispatches grooming intent) →
  back to fuzzing while the groom runs. Campaigns are detached; overlap them.
- Stop fuzzing a target when the last hour produced 0 new signatures and the
  seed set is exhausted — record that as a fact ("target saturated, N unique
  crashes, classes: ...") so the planner doesn't re-dispatch.
- Keep the crashing corpus: crash files are the repro inputs for every later
  step (grooming, reporting, retest).
