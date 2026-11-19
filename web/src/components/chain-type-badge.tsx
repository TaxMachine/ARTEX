// ChainTypeBadge classifies a finding by its primitives/chain anatomy:
// exploit-chain (chain present, every step confirmed) / hypothesis (chain with
// an assumed step) / primitives (facts recorded, no chain) / unset.
import { Badge } from "@/components/ui/badge";
import type { Finding } from "@/lib/types";

export type ChainType = "exploit-chain" | "hypothesis" | "primitives" | "unset";

export function chainTypeOf(
  f: Pick<Finding, "chain" | "primitives">,
): ChainType {
  const chain = f.chain ?? [];
  const prims = f.primitives ?? [];
  if (chain.length > 0) {
    return chain.every((c) => (c.result ?? "").trim()) ? "exploit-chain" : "hypothesis";
  }
  return prims.length > 0 ? "primitives" : "unset";
}

export function ChainTypeBadge({ finding }: { finding: Pick<Finding, "chain" | "primitives"> }) {
  const t = chainTypeOf(finding);
  switch (t) {
    case "exploit-chain":
      return (
        <Badge className="border-emerald-500/40 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400">
          exploit chain
        </Badge>
      );
    case "hypothesis":
      return (
        <Badge className="border-amber-500/40 bg-amber-500/10 text-amber-600 dark:text-amber-400">
          hypothesis
        </Badge>
      );
    case "primitives":
      return (
        <Badge className="border-slate-500/40 bg-slate-500/10 text-slate-600 dark:text-slate-400">
          primitives
        </Badge>
      );
    default:
      return <span className="text-xs text-muted-foreground">—</span>;
  }
}
