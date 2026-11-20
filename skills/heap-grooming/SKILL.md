---
name: heap-grooming
description: "Use for exploit PoCs that need deterministic heap manipulation: enabling full page heap to catch corruption at the exact instruction, spraying/clearing allocator size classes to position a victim allocation, and the observe-adjust loop that turns a crash primitive into a controlled write. Covers the page_heap and heap_spray tools with the windbg MCP for fault inspection."
mcps: windbg
---

# Heap grooming: from crash primitive to controlled write

(Upstream: `fuzz_campaign` finds the crashes this skill turns into chains —
see the fuzzing-triage skill for the find side.)

Complex PoCs (Office document parsers, kernel pool sprays, UAF reclaims) fail
as blind retries and succeed as a **loop**: instrument, groom, trigger, read
the fault, adjust. This skill is that loop.

## 1. Instrument first: `page_heap`

Full page heap makes the allocator fault **at the moment of corruption** —
first-chance, exact instruction — instead of crashing three hundred
allocations later. It is the difference between one-turn convergence and
fifty blind attempts.

```
page_heap(machine="office-vm", process="WINWORD.EXE", action="enable", mode="full")
```

- `full` — every allocation gets guard pages; the overflow faults immediately.
  Start here. The process MUST be (re)started after enabling.
- `silent` — page-heap bookkeeping, corruption detected on FREE, process keeps
  running. Use when full-page kills the process before your groom completes
  (long parse chains that must survive to the trigger).
- `action="status"` reads current settings; `action="disable"` removes
  (always disable when done — page heap changes timing and memory pressure,
  and a finding "confirmed under page heap" must be re-confirmed without it).

## 2. Groom: `heap_spray`

Deterministic heap positioning. The harness (a detached process on the VM)
allocates N blocks of a chosen size and holds them; you then trigger the
vulnerable path from the same task; `clear` frees everything.

```
heap_spray(machine="office-vm", action="spray", count=2000, size=0x78, filler="B")
# ...trigger via the PoC path (ssh_exec / COM open)...
heap_spray(machine="office-vm", action="status")
heap_spray(machine="office-vm", action="clear")
```

- `size` = the victim allocator's size class: derive it from the allocation
  the primitive corrupts (see the fault context in windbg), then spray that
  class so the freed/reclaimed slot is surrounded by your blocks.
- For Office: BSTR/`SAFEARRAY` sprays hit the process heap (LFH), document
  part allocations hit the OLE allocators — match the allocator that owns the
  victim, not just the size.
- Groom patterns worth knowing: **free-list poisoning position** (spray, free
  alternating blocks, trigger), **hole punching** (spray contiguous, free one
  in the middle for the victim to land in), **UAF reclaim** (free the victim,
  spray the size class so your block takes the slot).

## 3. Measure the heap: heap_query

Data-driven grooming instead of eyeball debugging:
```
heap_query(machine="office-vm", action="census")           # size classes, heaviest first
heap_query(machine="office-vm", action="allocations", size="0x78", state="busy")
heap_query(machine="office-vm", action="chunk", address="0x...")  # victim chunk + neighbours + stacks
```
census before/after a heap_spray proves the spray landed in the size class
you think it did; chunk at the corruption address shows exactly what the
overflow will hit. Session reuse: pass the session id from a previous call.

## 4. Read the fault: windbg MCP

When page_heap trips, attach (or open the dump) and read the corruption with
the windbg tools: faulting instruction, registers, and — the heap views —
`!heap -p -a <addr>` (page-heap block info: who allocated it, what
overwrote it), `!heap -stat`, `!heap -flt s <size>`. This tells you WHERE the
write landed and HOW FAR it went; adjust count/size/filler accordingly.

## 5. The loop and when to stop

```
enable page_heap → spray → trigger → read fault → adjust → repeat
```

Stop conditions, per the confirmation standard:
- The write lands where the chain needs it → **primitive confirmed** — record
  it (`primitives` on the finding: observed=true, evidence=the fault context).
- Corruption observed but placement never stabilizes → record the primitive
  with its grooming constraint ("requires 0x78-class LFH spray, placement
  stable in 3/10 runs") — that is a fact the next intent builds on, not a
  failure.
- A grooming-dependent finding must state its reliability as a measured
  number ("triggered 3/10 runs with 2000×0x78 spray"), same as every other
  impact claim.

## Notes

- Page heap and the spray harness change timing; the FINAL confirmation run
  should be `page_heap=disable` + natural allocation flow, then re-enable to
  demonstrate the fault if reviewers need the precise corruption point.
- The spray harness lives per-VM (`%TEMP%\artex-spray.json` reports state);
  `clear` between attempts, or the groom accumulates.
