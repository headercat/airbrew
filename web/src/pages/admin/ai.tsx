import { useEffect, useState } from "react";
import { Loader2, Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { PageWrapper } from "@/components/page";
import { SectionHeader, SettingRow } from "./shared";
import {
  type AdminAIAgent,
  type AIProvider,
  type Driver,
  adminCreateAgent,
  adminDeleteAgent,
  adminDeleteProvider,
  adminListAgents,
  adminListDrivers,
  adminListProviders,
  adminPutProvider,
  adminUpdateAgent,
} from "@/lib/ai";
import { isApiError } from "@/lib/api";
import { Bot } from "lucide-react";

export default function AdminAI() {
  return (
    <PageWrapper>
      <SectionHeader
        icon={Bot}
        titleKey="dashboard.modules.ai.label"
        descKey="dashboard.modules.ai.description"
      />
      <ProviderSection />
      <AgentsSection />
    </PageWrapper>
  );
}

// ---- Provider section ----

function ProviderSection() {
  const { t } = useTranslation();
  const [providers, setProviders] = useState<AIProvider[]>([]);
  const [drivers, setDrivers] = useState<Driver[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Form state (chat direction only for v1).
  const [driver, setDriver] = useState("openai");
  const [name, setName] = useState("");
  const [baseURL, setBaseURL] = useState("");
  const [model, setModel] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [saving, setSaving] = useState(false);

  async function load() {
    setLoading(true);
    try {
      const [provs, drvs] = await Promise.all([
        adminListProviders(),
        adminListDrivers(),
      ]);
      setProviders(provs);
      setDrivers(drvs);
      const active = provs.find((p) => p.direction === "chat" && p.is_active);
      if (active) {
        setDriver(active.driver);
        setName(active.name);
        setBaseURL(active.base_url);
        setModel(active.model_hint);
      } else if (drvs.length > 0) {
        const d = drvs.find((d) => d.name === "openai") ?? drvs[0];
        setDriver(d.name);
        setModel(d.default_model);
      }
    } catch (err) {
      setError(fmtErr(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    load();
  }, []);

  async function save() {
    setSaving(true);
    setError(null);
    try {
      await adminPutProvider("chat", {
        driver,
        name: name || undefined,
        base_url: baseURL || undefined,
        model_hint: model || undefined,
        api_key: apiKey || undefined,
      });
      setApiKey("");
      await load();
    } catch (err) {
      setError(fmtErr(err));
    } finally {
      setSaving(false);
    }
  }

  async function remove(id: string) {
    if (!confirm(t("admin.ai.confirmDeleteProvider"))) return;
    try {
      await adminDeleteProvider(id);
      await load();
    } catch (err) {
      setError(fmtErr(err));
    }
  }

  const activeDriver = drivers.find((d) => d.name === driver);

  return (
    <Card>
      <CardContent className="space-y-4 p-6">
        <div className="flex items-center justify-between">
          <div className="space-y-0.5">
            <p className="text-sm font-medium">
              {t("admin.ai.providerTitle")}
            </p>
            <p className="text-xs text-muted-foreground">
              {t("admin.ai.providerDesc")}
            </p>
          </div>
          {providers.find((p) => p.direction === "chat" && p.is_active) && (
            <Badge>{t("admin.ai.active")}</Badge>
          )}
        </div>

        {error && <p className="text-sm text-destructive">{error}</p>}

        {loading ? (
          <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
        ) : (
          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-2">
              <Label>{t("admin.ai.driver")}</Label>
              <select
                className="h-9 w-full rounded-md border border-border bg-background px-3 text-sm"
                value={driver}
                onChange={(e) => {
                  setDriver(e.target.value);
                  const d = drivers.find((d) => d.name === e.target.value);
                  if (d && !model) setModel(d.default_model);
                }}
              >
                {drivers.map((d) => (
                  <option key={d.name} value={d.name}>
                    {d.name}
                    {d.needs_api_key ? " (API key)" : ""}
                  </option>
                ))}
              </select>
            </div>
            <div className="space-y-2">
              <Label>{t("admin.ai.name")}</Label>
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t("admin.ai.namePlaceholder")}
              />
            </div>
            <div className="space-y-2">
              <Label>{t("admin.ai.baseURL")}</Label>
              <Input
                value={baseURL}
                onChange={(e) => setBaseURL(e.target.value)}
                placeholder="https://api.openai.com/v1"
              />
            </div>
            <div className="space-y-2">
              <Label>{t("admin.ai.model")}</Label>
              <Input
                value={model}
                onChange={(e) => setModel(e.target.value)}
                placeholder={activeDriver?.default_model ?? ""}
              />
            </div>
            <div className="space-y-2 md:col-span-2">
              <Label>
                {t("admin.ai.apiKey")}
                {activeDriver && !activeDriver.needs_api_key && (
                  <span className="ml-2 text-[10px] text-muted-foreground">
                    ({t("admin.ai.apiKeyOptional")})
                  </span>
                )}
              </Label>
              <Input
                type="password"
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
                placeholder={t("admin.ai.apiKeyPlaceholder")}
                autoComplete="off"
              />
            </div>
          </div>
        )}

        <div className="flex justify-end">
          <Button onClick={save} disabled={saving || loading}>
            {saving ? (
              <Loader2 className="mr-1 h-4 w-4 animate-spin" />
            ) : (
              <Plus className="mr-1 h-4 w-4" />
            )}
            {t("admin.ai.activate")}
          </Button>
        </div>

        {providers.length > 0 && (
          <div className="space-y-1 rounded-md border border-border">
            {providers.map((p) => (
              <SettingRow
                key={p.id}
                title={`${p.driver} — ${p.name}`}
                description={
                  p.model_hint
                    ? `${p.direction} • ${p.model_hint}${
                        p.has_api_key ? " • 🔑" : ""
                      }`
                    : `${p.direction}${p.has_api_key ? " • 🔑" : ""}`
                }
              >
                <div className="flex items-center gap-2">
                  {p.is_active && (
                    <Badge variant="default" className="text-[10px]">
                      {t("admin.ai.active")}
                    </Badge>
                  )}
                  <button
                    onClick={() => remove(p.id)}
                    className="text-muted-foreground hover:text-destructive"
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </button>
                </div>
              </SettingRow>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// ---- Agents section ----

function AgentsSection() {
  const { t } = useTranslation();
  const [agents, setAgents] = useState<AdminAIAgent[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState<AdminAIAgent | null>(null);

  async function load() {
    setLoading(true);
    try {
      setAgents(await adminListAgents());
    } catch (err) {
      setError(fmtErr(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    load();
  }, []);

  async function remove(id: string) {
    if (!confirm(t("admin.ai.confirmDeleteAgent"))) return;
    try {
      await adminDeleteAgent(id);
      await load();
    } catch (err) {
      setError(fmtErr(err));
    }
  }

  return (
    <Card>
      <CardContent className="space-y-4 p-6">
        <div className="flex items-center justify-between">
          <div className="space-y-0.5">
            <p className="text-sm font-medium">{t("admin.ai.agentsTitle")}</p>
            <p className="text-xs text-muted-foreground">
              {t("admin.ai.agentsDesc")}
            </p>
          </div>
          <Button
            size="sm"
            variant="secondary"
            onClick={() =>
              setEditing({
                id: "",
                name: "",
                description: "",
                model: "",
                tools: [],
                is_builtin: false,
                is_active: true,
                system_prompt: "",
                temperature: 0.7,
                max_tokens: 2048,
                max_turns: 6,
              })
            }
          >
            <Plus className="mr-1 h-4 w-4" />
            {t("admin.ai.newAgent")}
          </Button>
        </div>

        {error && <p className="text-sm text-destructive">{error}</p>}
        {loading ? (
          <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
        ) : editing ? (
          <AgentEditor
            agent={editing}
            onCancel={() => setEditing(null)}
            onSaved={() => {
              setEditing(null);
              load();
            }}
          />
        ) : (
          <div className="space-y-1 rounded-md border border-border">
            {agents.map((a) => (
              <SettingRow
                key={a.id}
                title={a.name + (a.is_builtin ? ` · ${t("admin.ai.builtin")}` : "")}
                description={`${a.model}${a.tools.length ? ` · ${a.tools.length} ${t("ai.tools")}` : ""}`}
              >
                <div className="flex items-center gap-2">
                  {a.is_active ? (
                    <Badge variant="default" className="text-[10px]">
                      {t("dashboard.online")}
                    </Badge>
                  ) : (
                    <Badge variant="outline" className="text-[10px]">
                      {t("dashboard.disabled")}
                    </Badge>
                  )}
                  <button
                    onClick={() => setEditing(a)}
                    className="text-xs text-primary hover:underline"
                  >
                    {t("common.edit")}
                  </button>
                  {!a.is_builtin && (
                    <button
                      onClick={() => remove(a.id)}
                      className="text-muted-foreground hover:text-destructive"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </button>
                  )}
                </div>
              </SettingRow>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function AgentEditor({
  agent,
  onCancel,
  onSaved,
}: {
  agent: AdminAIAgent;
  onCancel: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState(agent.name);
  const [description, setDescription] = useState(agent.description);
  const [model, setModel] = useState(agent.model);
  const [systemPrompt, setSystemPrompt] = useState(agent.system_prompt);
  const [tools, setTools] = useState(agent.tools.join(", "));
  const [temperature, setTemperature] = useState(String(agent.temperature));
  const [maxTokens, setMaxTokens] = useState(String(agent.max_tokens));
  const [maxTurns, setMaxTurns] = useState(String(agent.max_turns));
  const [isActive, setIsActive] = useState(agent.is_active);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function save() {
    setBusy(true);
    setError(null);
    const body = {
      name,
      description,
      model,
      system_prompt: systemPrompt,
      tools: tools
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean),
      temperature: parseFloat(temperature) || 0.7,
      max_tokens: parseInt(maxTokens, 10) || 0,
      max_turns: parseInt(maxTurns, 10) || 6,
      is_active: isActive,
    };
    try {
      if (agent.id) {
        await adminUpdateAgent(agent.id, body);
      } else {
        await adminCreateAgent(body);
      }
      onSaved();
    } catch (err) {
      setError(fmtErr(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-3 rounded-md border border-border p-4">
      {error && <p className="text-sm text-destructive">{error}</p>}
      <div className="grid gap-3 md:grid-cols-2">
        <Field label={t("admin.ai.agentName")}>
          <Input value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label={t("admin.ai.model")}>
          <Input value={model} onChange={(e) => setModel(e.target.value)} />
        </Field>
      </div>
      <Field label={t("admin.ai.description")}>
        <Input
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </Field>
      <Field label={t("admin.ai.systemPrompt")}>
        <Textarea
          value={systemPrompt}
          onChange={(e) => setSystemPrompt(e.target.value)}
          rows={6}
          className="font-mono text-xs"
        />
      </Field>
      <div className="grid gap-3 md:grid-cols-4">
        <Field label={t("admin.ai.tools")}>
          <Input
            value={tools}
            onChange={(e) => setTools(e.target.value)}
            placeholder="clock, echo"
          />
        </Field>
        <Field label={t("admin.ai.temperature")}>
          <Input
            type="number"
            step="0.1"
            value={temperature}
            onChange={(e) => setTemperature(e.target.value)}
          />
        </Field>
        <Field label={t("admin.ai.maxTokens")}>
          <Input
            type="number"
            value={maxTokens}
            onChange={(e) => setMaxTokens(e.target.value)}
          />
        </Field>
        <Field label={t("admin.ai.maxTurns")}>
          <Input
            type="number"
            value={maxTurns}
            onChange={(e) => setMaxTurns(e.target.value)}
          />
        </Field>
      </div>
      <div className="flex items-center justify-between">
        <label className="flex items-center gap-2 text-sm">
          <Switch checked={isActive} onCheckedChange={setIsActive} />
          {t("admin.moduleSettings.enabled")}
        </label>
        <div className="flex gap-2">
          <Button variant="ghost" size="sm" onClick={onCancel}>
            {t("common.back")}
          </Button>
          <Button size="sm" onClick={save} disabled={busy}>
            {busy && <Loader2 className="mr-1 h-4 w-4 animate-spin" />}
            {t("common.save")}
          </Button>
        </div>
      </div>
    </div>
  );
}

function Field({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1.5">
      <Label className="text-xs">{label}</Label>
      {children}
    </div>
  );
}

function fmtErr(err: unknown): string {
  if (isApiError(err)) return err.error_description ?? err.error;
  if (err instanceof Error) return err.message;
  return String(err);
}
