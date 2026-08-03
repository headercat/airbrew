import { api } from "@/lib/api";

export type WorkflowNodeConfig = Record<string, unknown>;

export type WorkflowNode = {
  id: string;
  type: string;
  config?: WorkflowNodeConfig;
};

export type WorkflowEdge = {
  from: string;
  to: string;
  port?: string;
};

export type WorkflowDefinition = {
  nodes: WorkflowNode[];
  edges: WorkflowEdge[];
};

export type Workflow = {
  id: string;
  user_id: string;
  name: string;
  description: string;
  definition: WorkflowDefinition;
  version: number;
  is_active: boolean;
  trigger_type?: string;
  webhook_token?: string;
  cron_expr?: string;
  created_at: string;
  updated_at: string;
};

export type WorkflowRun = {
  id: string;
  workflow_id: string;
  user_id: string;
  version: number;
  status: string;
  trigger: string;
  input_json: string;
  error?: string;
  started_at: string;
  finished_at?: string;
};

export type WorkflowStepRun = {
  id: string;
  run_id: string;
  node_id: string;
  node_type: string;
  status: string;
  input_json: string;
  output_json: string;
  error?: string;
  duration_ms: number;
  seq: number;
  started_at: string;
  finished_at?: string;
};

export const workflow = {
  list: async (args?: { limit?: number; offset?: number }) => {
    const qs = new URLSearchParams();
    if (args?.limit) qs.set("limit", String(args.limit));
    if (args?.offset) qs.set("offset", String(args.offset));
    const res = await api.get<{ workflows: Workflow[] }>(
      `/api/workflow/workflows${qs.size ? `?${qs}` : ""}`,
    );
    return res.workflows ?? [];
  },
  get: (id: string) => api.get<Workflow>(`/api/workflow/workflows/${id}`),
  create: (body: {
    name: string;
    description?: string;
    definition: WorkflowDefinition;
  }) => api.post<Workflow>("/api/workflow/workflows", body),
  update: (
    id: string,
    body: {
      name?: string;
      description?: string;
      definition?: WorkflowDefinition;
    },
  ) => api.patch<Workflow>(`/api/workflow/workflows/${id}`, body),
  remove: (id: string) =>
    api.del<{ ok: boolean }>(`/api/workflow/workflows/${id}`),
  activate: (id: string) =>
    api.post<Workflow>(`/api/workflow/workflows/${id}/activate`, {}),
  deactivate: (id: string) =>
    api.post<Workflow>(`/api/workflow/workflows/${id}/deactivate`, {}),
  run: (id: string, input?: Record<string, unknown>) =>
    api.post<WorkflowRun>(`/api/workflow/workflows/${id}/run`, {
      input: input ?? {},
    }),
  listRuns: async (args?: { limit?: number; offset?: number }) => {
    const qs = new URLSearchParams();
    if (args?.limit) qs.set("limit", String(args.limit));
    if (args?.offset) qs.set("offset", String(args.offset));
    const res = await api.get<{ runs: WorkflowRun[] }>(
      `/api/workflow/runs${qs.size ? `?${qs}` : ""}`,
    );
    return res.runs ?? [];
  },
  getRun: (id: string) => api.get<WorkflowRun>(`/api/workflow/runs/${id}`),
  listSteps: async (id: string) => {
    const res = await api.get<{ steps: WorkflowStepRun[] }>(
      `/api/workflow/runs/${id}/steps`,
    );
    return res.steps ?? [];
  },
};

// A minimal valid definition so the Create button can seed the editor without
// the user having to remember the node catalog.
export function starterDefinition(): WorkflowDefinition {
  return {
    nodes: [
      { id: "start", type: "trigger.manual" },
      { id: "log", type: "action.log", config: { message: "hello" } },
    ],
    edges: [{ from: "start", to: "log" }],
  };
}
