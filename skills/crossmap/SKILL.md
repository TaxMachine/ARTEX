---
name: crossmap
description: "Use BEFORE opening IDA or hand-resolving any address: the cross-binary function map of every analyzed binary in the lab. Function search, cross-binary callers/callees with imports and RVAs already resolved to real symbols, exploit-chain paths, cached decompiles. Covers 'which function parses this', 'who calls this export', 'how do I get from this IOCTL handler to that privileged operation' questions."
mcps: crossmap
---

# Crossmap — the cross-binary function map

ARTEX ships a precomputed function graph of every binary the lab has analyzed
(drivers, DLLs, the kernel, Office components). **Import thunks, IAT entries and
raw RVAs are already resolved into real edges to the exporting module's
symbol.** Never hand-resolve an RVA or import yourself; never open IDA for a
question the map answers.

Tools (MCP `crossmap`, bound to every pentest role):

## 0. Missing module? Index it yourself — `map_harvest`

If `map_binaries` shows your target module is NOT in the map (a crash in an
unindexed DLL, a new component of the attack surface), do not wait for the
operator:

```
map_harvest(binary="msxml6.dll", vm_path="C:/Windows/System32/msxml6.dll")
map_harvest_status(job="h1-...")   # poll: queued|scp|analyze|index|done|error
```

This stages the binary from the VM, runs IDA batch analysis (idat -B —
MINUTES TO HOURS for big binaries like EXCEL.EXE/WWLIB.DLL; start it early and
do other work), extracts the function graph and ingests into crossmap. Poll
periodically; when done, the module is queryable by every map tool and crash
offsets symbolize automatically. Dedup: a running job for the same binary is
returned instead of a second one.

## 1. Find functions — `map_search`

```
map_search(q="ClfsAddLogContainer", limit=25)
map_search(q="IoCreateDevice", binary="ntoskrnl.exe", limit=10)
```

Names are IDA names: named exports (`ClfsAddLogContainer`), `sub_XXXXXX` for
unnamed code, `j__`-prefixed thunks. Filter to one binary with `binary=`.

## 2. Who calls / what does it call — `map_node`

```
map_node(ref="CLFS.sys!ClfsAddLogContainer")
```

Returns the function's cross-binary callers and callees **with edge kinds**:
`call` = internal, `import` = a cross-module edge landing on the exporter's
real symbol (e.g. `afd.sys!X → ntoskrnl.exe!IofCallDriver`). The IAT/RVA work
is done; the callee you see for an indirect call is the actual target export.

## 3. Exploit-chain query — `map_paths`

```
map_paths(frm="afd_9818.sys!AfdInitialize", to="ntoskrnl.exe!IoCreateDevice", max_hops=5)
```

Shortest path between two functions **across binaries** — how a reachable
entry point reaches a privileged operation through resolved import edges. This
is the reachability argument for a chain finding.

## 4. Read the code — `map_decompile`

```
map_decompile(ref="CLFS.sys!ClfsAddLogContainer")
```

Cached IDA pseudocode — reading a chain step costs milliseconds and needs no
IDA session. Use it instead of re-opening the database.

## 5. Coverage — `map_binaries` / `map_stats`

`map_binaries()` lists every indexed binary with function/entry counts (the
join keys a project's `binaries` list uses); `map_stats()` gives total
nodes/edges. If a binary is **not** indexed, say so in a fact (the operator
can harvest it), and fall back to in-VM tooling.

## Workflow rules

- Prefer the map over live IDA for every lookup, xref, import and path
  question; IDA (`ida` MCP) is for interactive work the map can't answer
  (renaming, structures, debugging a specific hypothesis).
- When reporting a finding, cite functions as `binary!function` — the same
  refs the map and the project page use.
- When a task stages a new binary version, register it as a binary asset
  (insert_assets, type=binary, version=build) so the project's patch-diff
  pairs and map coverage stay coherent.
