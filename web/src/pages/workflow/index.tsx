import { useCallback, useEffect, useState } from "react";
import {
  AlertCircle,
  ChevronLeft,
  Loader2,
  Play,
  Plus,
  Power,
  Save,
  Trash2,
} from "lucide-react";
import { useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Modal } from "@/components/ui/modal";
import { Textarea } from "@/components/ui/textarea";
import { PageHeader, PageWrapper } from "@/components/page";
import { isApiError } from "@/lib/api";
import {
  starterDefinition,
  workflow,
  type Workflow,
  type WorkflowDefinition,
  type WorkflowRun,
  type WorkflowStepRun,
} from "@/lib/workflow";
import { cn } from "@/lib/utils";

type Tab = "flows" | "runs";

export default function WorkflowPage() {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const tab: Tab = params.get("view") === "runs" ? "runs" : "flows";

  return (
    <PageWrapper>
      <PageHeader
        title={t("workflow.title")}
        description={t("workflow.description")}
      />
      <div className="flex gap-2 border-b">
        {(["flows", "runs"] as const).map((k) => (
          <button
            key={k}
            onClick={() => setParams(k === "flows" ? {} : { view: "runs" })}
            className={cn(
              "-mb-px border-b-2 px-3 py-2 text-sm font-medium",
              tab === k
                ? "border-primary text-primary"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {t(`workflow.tabs.${k}`)}
          </button>
        ))}
      </div>
      {tab === "flows" ? <FlowsView /> : <RunsView />}
    </PageWrapper>
  );
}

// ---------------------------------------------------------------------------
// Flows
// ---------------------------------------------------------------------------

const EMPTY_DEF: WorkflowDefinition = { nodes: [], edges: [] };

function FlowsView() {
  const { t } = useTranslation();
  const [flows, setFlows] = useState<Workflow[]>([]);
  const [selectedID, setSelectedID] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);

  const refresh = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const items = await workflow.list({ limit: 100 });
      setFlows(items);
      setSelectedID((prev) =>
        prev && items.some((f) => f.id === prev) ? prev : items[0]?.id ?? "",
      );
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const selected = flows.find((f) => f.id === selectedID) ?? null;

  return (
    <div className="grid gap-6 md:grid-cols-[280px_1fr]">
      <aside className="space-y-2">
        <Button
          className="w-full justify-start"
          variant="outline"
          onClick={() => setCreateOpen(true)}
        >
          <Plus className="mr-2 h-4 w-4" />
          {t("workflow.create")}
        </Button>
        {loading ? (
          <p className="px-2 py-4 text-sm text-muted-foreground">
            {t("common.loading")}
          </p>
        ) : flows.length === 0 ? (
          <p className="px-2 py-4 text-sm text-muted-foreground">
            {t("workflow.noFlows")}
          </p>
        ) : (
          <ul className="space-y-1">
            {flows.map((f) => (
              <li key={f.id}>
                <button
                  onClick={() => setSelectedID(f.id)}
                  className={cn(
                    "block w-full rounded-md px-3 py-2 text-left text-sm hover:bg-accent/50",
                    selectedID === f.id && "bg-accent",
                  )}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="truncate font-medium">{f.name}</span>
                    {f.is_active && (
                      <Badge variant="secondary" className="text-[10px]">
                        {t("workflow.active")}
                      </Badge>
                    )}
                  </div>
                  <div className="truncate text-xs text-muted-foreground">
                    {f.trigger_type || t("workflow.draft")}
                  </div>
                </button>
              </li>
            ))}
          </ul>
        )}
      </aside>

      <section>
        {error && (
          <p className="mb-3 rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
            {error}
          </p>
        )}
        {selected ? (
          <FlowEditor key={selected.id} flow={selected} onChanged={refresh} />
        ) : (
          <p className="px-2 py-10 text-center text-sm text-muted-foreground">
            {t("workflow.selectFlow")}
          </p>
        )}
      </section>

      <CreateFlowModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={(id) => {
          setCreateOpen(false);
          void refresh().then(() => setSelectedID(id));
        }}
      />
    </div>
  );
}

function FlowEditor({
  flow,
  onChanged,
}: {
  flow: Workflow;
  onChanged: () => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState(flow.name);
  const [description, setDescription] = useState(flow.description);
  const [defText, setDefText] = useState(
    JSON.stringify(flow.definition ?? EMPTY_DEF, null, 2),
  );
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [runResult, setRunResult] = useState<WorkflowRun | null>(null);

  useEffect(() => {
    setName(flow.name);
    setDescription(flow.description);
    setDefText(JSON.stringify(flow.definition ?? EMPTY_DEF, null, 2));
    setErr(null);
    setRunResult(null);
  }, [flow]);

  const save = async () => {
    setBusy(true);
    setErr(null);
    try {
      const parsed = JSON.parse(defText) as WorkflowDefinition;
      await workflow.update(flow.id, { name, description, definition: parsed });
      onChanged();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const toggle = async () => {
    setBusy(true);
    setErr(null);
    try {
      if (flow.is_active) await workflow.deactivate(flow.id);
      else await workflow.activate(flow.id);
      onChanged();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!confirm(t("workflow.confirmDelete"))) return;
    setBusy(true);
    setErr(null);
    try {
      await workflow.remove(flow.id);
      onChanged();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const run = async () => {
    setBusy(true);
    setErr(null);
    setRunResult(null);
    try {
      // Validate JSON before running so a malformed graph is caught here
      // rather than as a 500.
      JSON.parse(defText);
      const res = await workflow.run(flow.id, {});
      setRunResult(res);
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0 flex-1 space-y-2">
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            className="h-9 text-base font-semibold"
          />
          <Input
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder={t("workflow.descriptionPlaceholder")}
            className="h-8 text-sm"
          />
        </div>
        <div className="flex shrink-0 gap-2">
          <Button variant="outline" size="sm" onClick={run} disabled={busy}>
            <Play className="mr-1 h-4 w-4" />
            {t("workflow.run")}
          </Button>
          <Button
            variant={flow.is_active ? "secondary" : "default"}
            size="sm"
            onClick={toggle}
            disabled={busy}
          >
            <Power className="mr-1 h-4 w-4" />
            {flow.is_active ? t("workflow.deactivate") : t("workflow.activate")}
          </Button>
        </div>
      </div>

      <div className="flex items-center justify-between">
        <p className="text-sm font-medium">{t("workflow.definition")}</p>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={save} disabled={busy}>
            <Save className="mr-1 h-4 w-4" />
            {t("workflow.save")}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={remove}
            disabled={busy}
            className="text-destructive"
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </div>

      {flow.webhook_token && (
        <p className="rounded-md bg-muted px-3 py-2 font-mono text-xs">
          {t("workflow.webhookUrl")}: /api/workflow/hooks/{flow.webhook_token}
        </p>
      )}
      {flow.cron_expr && (
        <p className="rounded-md bg-muted px-3 py-2 font-mono text-xs">
          {t("workflow.cron")}: {flow.cron_expr}
        </p>
      )}

      <Textarea
        value={defText}
        onChange={(e) => setDefText(e.target.value)}
        className="min-h-[320px] font-mono text-xs"
        spellCheck={false}
      />

      {err && (
        <p className="flex items-start gap-2 rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
          {err}
        </p>
      )}
      {runResult && (
        <p className="rounded-md bg-muted px-3 py-2 text-xs">
          {t("workflow.runStarted")}:{" "}
          <span className="font-mono">{runResult.status}</span> ·{" "}
          {runResult.id}
        </p>
      )}
      <p className="text-xs text-muted-foreground">{t("workflow.nodeHint")}</p>
    </div>
  );
}

function CreateFlowModal({
  open,
  onClose,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: (id: string) => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setName("");
      setErr(null);
    }
  }, [open]);

  const submit = async () => {
    setBusy(true);
    setErr(null);
    try {
      const created = await workflow.create({
        name: name.trim() || t("workflow.untitled"),
        definition: starterDefinition(),
      });
      onCreated(created.id);
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open={open} onClose={onClose} title={t("workflow.create")}>
      <div className="space-y-3">
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={t("workflow.namePlaceholder")}
          autoFocus
        />
        {err && <p className="text-sm text-destructive">{err}</p>}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button onClick={submit} disabled={busy}>
            {busy && <Loader2 className="mr-1 h-4 w-4 animate-spin" />}
            {t("workflow.create")}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

function RunsView() {
  const { t } = useTranslation();
  const [runs, setRuns] = useState<WorkflowRun[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [openRun, setOpenRun] = useState<WorkflowRun | null>(null);

  const refresh = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      setRuns(await workflow.listRuns({ limit: 50 }));
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  if (openRun) {
    return <RunDetail run={openRun} onBack={() => setOpenRun(null)} />;
  }

  return (
    <div className="space-y-3">
      {error && (
        <p className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {error}
        </p>
      )}
      {loading ? (
        <p className="py-6 text-center text-sm text-muted-foreground">
          <Loader2 className="mx-auto h-4 w-4 animate-spin" />
        </p>
      ) : runs.length === 0 ? (
        <p className="py-10 text-center text-sm text-muted-foreground">
          {t("workflow.noRuns")}
        </p>
      ) : (
        <ul className="divide-y rounded-md border">
          {runs.map((r) => (
            <li key={r.id}>
              <button
                onClick={() => setOpenRun(r)}
                className="flex w-full items-center gap-3 px-3 py-3 text-left hover:bg-accent/40"
              >
                <RunStatusBadge status={r.status} />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium">
                    {fmtDate(r.started_at)}
                  </div>
                  <div className="truncate text-xs text-muted-foreground">
                    {t("workflow.workflow")}: {r.workflow_id} · {r.trigger}
                  </div>
                </div>
                {r.error && (
                  <span className="truncate text-xs text-destructive">
                    {r.error}
                  </span>
                )}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function RunDetail({
  run,
  onBack,
}: {
  run: WorkflowRun;
  onBack: () => void;
}) {
  const { t } = useTranslation();
  const [steps, setSteps] = useState<WorkflowStepRun[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    workflow
      .listSteps(run.id)
      .then((s) => {
        if (alive) setSteps(s);
      })
      .catch((e) => {
        if (alive) setError(errMsg(e));
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [run.id]);

  return (
    <div className="space-y-4">
      <button
        onClick={onBack}
        className="flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
      >
        <ChevronLeft className="h-4 w-4" />
        {t("workflow.backToRuns")}
      </button>
      <div className="flex items-center gap-3">
        <RunStatusBadge status={run.status} />
        <span className="font-mono text-sm">{run.id}</span>
        <span className="text-xs text-muted-foreground">
          {fmtDate(run.started_at)}
        </span>
      </div>
      {run.error && (
        <pre className="overflow-auto rounded-md bg-destructive/10 px-3 py-2 text-xs text-destructive">
          {run.error}
        </pre>
      )}
      {error && (
        <p className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {error}
        </p>
      )}
      {loading ? (
        <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
      ) : steps.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("workflow.noSteps")}</p>
      ) : (
        <ol className="space-y-2">
          {steps.map((s) => (
            <li key={s.id} className="rounded-md border p-3">
              <div className="flex items-center gap-2">
                <RunStatusBadge status={s.status} />
                <span className="font-mono text-sm">{s.node_id}</span>
                <Badge variant="outline" className="text-[10px]">
                  {s.node_type}
                </Badge>
                <span className="ml-auto text-xs text-muted-foreground">
                  {s.duration_ms}ms
                </span>
              </div>
              {s.error && (
                <pre className="mt-2 overflow-auto rounded bg-destructive/10 px-2 py-1 text-xs text-destructive">
                  {s.error}
                </pre>
              )}
              <details className="mt-2">
                <summary className="cursor-pointer text-xs text-muted-foreground">
                  {t("workflow.io")}
                </summary>
                <pre className="mt-1 overflow-auto rounded bg-muted px-2 py-1 text-[11px]">
                  {t("workflow.input")}: {prettyJSON(s.input_json)}
                  {"\n"}
                  {t("workflow.output")}: {prettyJSON(s.output_json)}
                </pre>
              </details>
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}

function RunStatusBadge({ status }: { status: string }) {
  const variant =
    status === "success"
      ? "secondary"
      : status === "failed" ||
          status === "cancelled" ||
          status === "timed_out"
        ? "destructive"
        : "outline";
  return (
    <Badge variant={variant} className="text-[10px]">
      {status}
    </Badge>
  );
}

// ---- helpers ----

function errMsg(e: unknown): string {
  if (isApiError(e)) return e.error_description ?? e.error;
  if (e instanceof Error) return e.message;
  return "Unknown error";
}

function fmtDate(s: string): string {
  try {
    return new Date(s).toLocaleString();
  } catch {
    return s;
  }
}

function prettyJSON(s: string): string {
  if (!s) return "";
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}
