"use client";

import * as React from "react";

import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";

/**
 * DataTableShell is the one scrollable table container the function pages use:
 * a header row of controls, then a card whose body scrolls vertically while the
 * table head stays sticky. It exists so every table page (web traffic, machine
 * traffic, machines, findings, …) shares one scrolling implementation instead
 * of per-page overflow divs that drift apart.
 *
 * The page supplies the height strategy: pass `fill` to take the remaining
 * viewport (a flex-1 page layout), or an explicit className (e.g. h-[…]).
 *
 * Usage:
 *   <DataTableShell toolbar={<>…buttons…</>}>
 *     <Table>…sticky header via TableHeader className="sticky top-0 z-10 bg-card"…</Table>
 *   </DataTableShell>
 */
export function DataTableShell({
  toolbar,
  children,
  className,
  fill = false,
  bodyClassName,
  "aria-label": ariaLabel,
}: {
  /** Controls above the table (buttons, filters). Rendered in a wrap row. */
  toolbar?: React.ReactNode;
  /** The table itself. Put className="sticky top-0 z-10 bg-card" on its TableHeader. */
  children: React.ReactNode;
  /** Sizing of the outer container (fill=true sizes it to the remaining page height). */
  className?: string;
  /** When true the shell stretches to fill the remaining flex column height. */
  fill?: boolean;
  /** Extra classes on the scrolling body (rarely needed). */
  bodyClassName?: string;
  "aria-label"?: string;
}) {
  return (
    <div className={cn("flex min-h-0 flex-col", fill ? "flex-1" : "h-[calc(100vh-15rem)]", className)} aria-label={ariaLabel}>
      {toolbar ? <div className="mb-2 flex flex-wrap items-center gap-2">{toolbar}</div> : null}
      <Card className="flex min-h-0 flex-1 flex-col overflow-hidden py-0">
        <div className={cn("min-h-0 flex-1 overflow-auto", bodyClassName)}>{children}</div>
      </Card>
    </div>
  );
}