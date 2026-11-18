"use client";

import * as React from "react";
import Link from "next/link";

import { ArrowLeftIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { ProjectGraphView } from "@/components/project-graph";

export default function ProjectGraphPage() {
  const [key, setKey] = React.useState<string>("");
  const [name, setname] = React.useState<string>("");

  React.useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const k = params.get("key") ?? "";
    const n = params.get("name") ?? k;
    setKey(k);
    setname(n);
    document.title = `${n} · graph`;
  }, []);

  if (!key) {
    return (
      <div className="p-8 text-center text-muted-foreground">
        Missing ?key= — open a project from the <Link className="underline" href="/function/projects">Projects page</Link>.
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      <div className="flex items-center gap-3">
        <Button variant="ghost" size="icon" aria-label="Back to projects" asChild>
          <Link href="/function/projects">
            <ArrowLeftIcon />
          </Link>
        </Button>
        <div>
          <h1 className="text-xl font-semibold">{name}</h1>
          <p className="text-sm text-muted-foreground">
            every asset, finding, task and indexed function of this project — drag nodes, zoom, click for detail
          </p>
        </div>
      </div>
      <ProjectGraphView projectKey={key} />
    </div>
  );
}