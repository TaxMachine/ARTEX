"use client";

import * as React from "react";

import { KeyRoundIcon, Loader2Icon, PlugZapIcon, RefreshCwIcon, SearchIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";

// Provider catalog powering the create flow: pick a provider (or custom), then
// (preset path) paste a key, test, discover models and create profiles for the
// selected ones — or (custom path) open the manual editor.
export type ProviderPreset = {
  id: string;
  label: string;
  format: "anthropic" | "openai" | "openai-responses" | "ollama";
  baseUrl: string;
  nameHint: string;
  maxTokensField?: string;
  note?: string; // full note shown in the connect dialog
  blurb?: string; // one short line for the picker card (no overflow)
};

export const PROVIDER_PRESETS: ProviderPreset[] = [
  {
    id: "opencode-zen",
    label: "opencode zen",
    format: "openai",
    baseUrl: "https://opencode.ai/zen/v1",
    nameHint: "opencode zen",
    blurb: "Gateway: claude, gpt, glm, kimi, qwen…",
    note: "OpenAI-compatible gateway (claude, gpt, glm, kimi, qwen, deepseek…). Key: opencode auth (provider opencode-go) — the same key works here.",
  },
  {
    id: "nvidia-nim",
    label: "NVIDIA NIM",
    format: "openai",
    baseUrl: "https://integrate.api.nvidia.com/v1",
    nameHint: "NVIDIA NIM",
    blurb: "Hosted open models (gemma, mistral…)",
    note: "OpenAI-compatible. Key: build.nvidia.com (also stored by opencode auth as nvidia).",
  },
  {
    id: "openai",
    label: "OpenAI",
    format: "openai",
    baseUrl: "https://api.openai.com/v1",
    nameHint: "OpenAI",
    maxTokensField: "max_completion_tokens",
    blurb: "GPT / o-series models",
    note: "o-series / GPT-5 reasoning models need max_completion_tokens; classic models keep max_tokens.",
  },
  {
    id: "anthropic",
    label: "Anthropic",
    format: "anthropic",
    baseUrl: "https://api.anthropic.com",
    nameHint: "Anthropic",
    blurb: "Claude models (Messages API)",
    note: "Messages API. max_tokens is mandatory — set a per-reply cap on the profile.",
  },
  {
    id: "deepseek",
    label: "DeepSeek",
    format: "openai",
    baseUrl: "https://api.deepseek.com/v1",
    nameHint: "DeepSeek",
    blurb: "DeepSeek chat / reasoner",
    note: "OpenAI-compatible; also serves an /anthropic Messages path.",
  },
  {
    id: "openrouter",
    label: "OpenRouter",
    format: "openai",
    baseUrl: "https://openrouter.ai/api/v1",
    nameHint: "OpenRouter",
    blurb: "Aggregator, hundreds of models",
    note: "OpenAI-compatible aggregator; model ids carry vendor/ prefixes.",
  },
  {
    id: "ollama",
    label: "Ollama (local)",
    format: "ollama",
    baseUrl: "http://127.0.0.1:11434/v1",
    nameHint: "Ollama local",
    blurb: "Local server, keyless",
    note: "Keyless local server; connect works without a key.",
  },
];

// ProviderPickerDialog: the first modal the Create button opens. Provider
// cards go to the connect flow; Custom opens the manual editor.
export function ProviderPickerDialog({
  open,
  onOpenChange,
  onPick,
  onCustom,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onPick: (p: ProviderPreset) => void;
  onCustom: () => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Add models</DialogTitle>
          <DialogDescription>
            Pick a provider to connect with an API key (models are discovered automatically), or add a custom profile
            by hand.
          </DialogDescription>
        </DialogHeader>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
          {PROVIDER_PRESETS.map((p) => (
            <button
              key={p.id}
              type="button"
              onClick={() => onPick(p)}
              className="flex min-w-0 flex-col items-start gap-1 rounded-lg border p-3 text-left transition-colors hover:border-foreground/30 hover:bg-accent"
            >
              <span className="text-sm font-medium">{p.label}</span>
              <span className="w-full truncate text-[11px] leading-snug text-muted-foreground" title={p.note}>
                {p.blurb ?? p.baseUrl}
              </span>
            </button>
          ))}
          <button
            type="button"
            onClick={onCustom}
            className="flex min-w-0 flex-col items-start gap-1 rounded-lg border border-dashed p-3 text-left transition-colors hover:border-foreground/30 hover:bg-accent"
          >
            <span className="text-sm font-medium">Custom…</span>
            <span
              className="w-full truncate text-[11px] leading-snug text-muted-foreground"
              title="Manual base URL / format / headers — any OpenAI- or Anthropic-compatible endpoint"
            >
              Any OpenAI- or Anthropic-compatible endpoint
            </span>
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

// ConnectDialog: the preset flow — key in, test connection, discover models,
// create a profile per selected model (or all). Multiple keys per provider
// simply means connecting again with another key: each saved profile carries
// its own key.
export function ConnectDialog({
  preset,
  open,
  onOpenChange,
  onCustomize,
  onConnected,
}: {
  preset: ProviderPreset | null;
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onCustomize: (p: ProviderPreset, apiKey: string, model: string) => void;
  onConnected: () => void;
}) {
  if (!preset) return <Dialog open={open} onOpenChange={onOpenChange}><DialogContent className="sm:max-w-md" /></Dialog>;
  return (
    <ConnectInner
      key={preset.id}
      preset={preset}
      open={open}
      onOpenChange={onOpenChange}
      onCustomize={onCustomize}
      onConnected={onConnected}
    />
  );
}

function ConnectInner({
  preset,
  open,
  onOpenChange,
  onCustomize,
  onConnected,
}: {
  preset: ProviderPreset;
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onCustomize: (p: ProviderPreset, apiKey: string, model: string) => void;
  onConnected: () => void;
}) {
  const [apiKey, setApiKey] = React.useState("");
  const [testing, setTesting] = React.useState(false);
  const [loadingModels, setLoadingModels] = React.useState(false);
  const [models, setModels] = React.useState<string[]>([]);
  const [query, setQuery] = React.useState("");
  const [selected, setSelected] = React.useState<Set<string>>(new Set());
  const [creating, setCreating] = React.useState(false);

  React.useEffect(() => {
    if (open) return;
    // reset per close so a different provider/key starts clean
    setApiKey("");
    setModels([]);
    setQuery("");
    setSelected(new Set());
  }, [open]);

  async function testConnection() {
    if (testing) return;
    setTesting(true);
    try {
      const r = await api.testLLM(preset.format, "", preset.baseUrl, apiKey, "", "", "", undefined, true, "", "");
      if (r.ok) toast.success(`Connected · ${r.latency_ms ?? "?"}ms`);
      else toast.error(`Connection failed: ${r.error ?? "unknown"}`);
    } catch (e) {
      toast.error(`Test error: ${(e as Error).message}`);
    } finally {
      setTesting(false);
    }
  }

  async function discover() {
    if (loadingModels) return;
    setLoadingModels(true);
    setModels([]);
    try {
      const r = await api.fetchLLMModels(preset.format, preset.baseUrl, apiKey, "");
      if (r.ok && r.models?.length) {
        setModels(r.models);
        toast.success(`${r.models.length} models available`);
      } else {
        toast.error(`Failed to load models: ${r.error ?? "none returned"}`);
      }
    } catch (e) {
      toast.error(`Error: ${(e as Error).message}`);
    } finally {
      setLoadingModels(false);
    }
  }

  async function connect() {
    // one round trip: test, then discover
    await testConnection();
    await discover();
  }

  function toggle(m: string) {
    setSelected((prev) => {
      const n = new Set(prev);
      if (n.has(m)) n.delete(m);
      else n.add(m);
      return n;
    });
  }

  async function createProfiles() {
    const list = selected.size > 0 ? [...selected] : models;
    if (list.length === 0 || creating) return;
    setCreating(true);
    try {
      for (const m of list) {
        await api.saveLLMProfile({
          name: `${preset.nameHint} · ${m}`,
          format: preset.format,
          model: m,
          base_url: preset.baseUrl,
          api_key: apiKey,
          provider: preset.id,
          streaming: true,
          ...(preset.maxTokensField ? { max_tokens_field: preset.maxTokensField } : {}),
        });
      }
      toast.success(
        selected.size > 0
          ? `Created ${selected.size} profile${selected.size > 1 ? "s" : ""}`
          : `Created ${models.length} profiles (${preset.label})`,
      );
      onConnected();
      onOpenChange(false);
    } catch (e) {
      toast.error(`Create failed: ${(e as Error).message}`);
    } finally {
      setCreating(false);
    }
  }

  const filtered = models.filter((m) => m.toLowerCase().includes(query.trim().toLowerCase()));

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85vh] flex-col gap-4 sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <PlugZapIcon className="size-4" /> Connect {preset.label}
          </DialogTitle>
          <DialogDescription>
            {preset.note} Each connection saves profiles carrying this key — connect again with another key to add
            parallel access to the same provider.
          </DialogDescription>
        </DialogHeader>

        <div className="grid gap-2">
          <Label htmlFor="conn-key">API key</Label>
          <div className="flex gap-2">
            <Input
              id="conn-key"
              type="password"
              placeholder={preset.format === "ollama" ? "optional — local server runs keyless" : "paste the API key"}
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && void connect()}
            />
            <Button onClick={() => void connect()} disabled={testing || loadingModels}>
              {testing || loadingModels ? <Loader2Icon className="size-4 animate-spin" /> : <KeyRoundIcon className="size-4" />}
              Connect
            </Button>
          </div>
          <p className="text-[11px] text-muted-foreground break-all">
            Endpoint: <code>{preset.baseUrl}</code> · format <code>{preset.format}</code>
          </p>
        </div>

        {models.length > 0 && (
          <>
            <div className="flex items-center gap-2">
              <Input
                className="h-8"
                placeholder={`filter ${models.length} models…`}
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
              <Badge variant="outline" className="shrink-0">
                {selected.size > 0 ? `${selected.size} selected` : "all will be created"}
              </Badge>
              <Button size="sm" variant="ghost" className="shrink-0" onClick={() => setSelected(new Set())}>
                Clear
              </Button>
            </div>
            <div className="min-h-0 flex-1 space-y-1 overflow-y-auto rounded-lg border p-2">
              {filtered.map((m) => {
                const on = selected.has(m);
                return (
                  <button
                    key={m}
                    type="button"
                    onClick={() => toggle(m)}
                    className={cn(
                      "flex w-full items-center justify-between rounded-md border px-2.5 py-1.5 text-left font-mono text-xs hover:bg-accent",
                      on && "border-primary bg-primary/10",
                    )}
                  >
                    <span className="min-w-0 truncate">{m}</span>
                    {on && <Badge className="ml-2 shrink-0 text-[10px]">selected</Badge>}
                  </button>
                );
              })}
              {filtered.length === 0 && (
                <p className="py-4 text-center text-xs text-muted-foreground">no model matches the filter</p>
              )}
            </div>
            <div className="flex items-center justify-between gap-2">
              <Button
                size="sm"
                variant="ghost"
                onClick={() => onCustomize(preset, apiKey, selected.size === 1 ? [...selected][0] : "")}
              >
                <SearchIcon className="size-3.5" /> Advanced (full editor)
              </Button>
              <Button onClick={() => void createProfiles()} disabled={creating}>
                {creating ? <Loader2Icon className="size-4 animate-spin" /> : null}
                {selected.size > 0 ? `Create ${selected.size} profiles` : `Create all ${models.length} profiles`}
              </Button>
            </div>
          </>
        )}
        {models.length === 0 && !loadingModels && (
          <div className="flex items-center gap-2 rounded-lg border border-dashed p-4 text-xs text-muted-foreground">
            <RefreshCwIcon className="size-3.5" /> After connecting, the available models are listed here — select the
            ones to register as profiles.
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
