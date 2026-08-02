# Workflow module

A trigger/action automation engine: the user composes a directed graph of
**nodes** (a trigger followed by one or more logic/action steps) in JSON; the
runtime validates the graph, listens for triggers (webhooks, cron schedules,
manual runs), and walks the graph, threading an execution context that lets
later nodes read earlier nodes' output.

Think a tiny n8n/Zapier built into the single binary: webhook receivers,
scheduled cron triggers, conditional branching, HTTP requests, delay/sleep,
mutable variables, full run + step-level history for debugging.

## Data model

Migration `0014_workflow.sql`. All PKs use `internal/id`; `DATETIME` defaults
and `INTEGER` booleans follow existing convention.

- **`workflows`** — one user-defined automation. `definition` stores the
  versioned JSON graph (nodes + edges); `is_active` controls whether triggers
  fire. `version` is bumped on every definition update so callers can detect
  stale drafts; the previous definition is mirrored into `workflow_versions`
  for cheap history.
- **`workflow_versions`** — append-only snapshots of `definition` on every
  save, so an admin can roll back or diff what changed.
- **`workflow_runs`** — one row per execution (manual, webhook, schedule).
  `status` ∈ `pending`/`running`/`success`/`failed`/`cancelled`/`timed_out`;
  `trigger` records what kicked it off; `error` holds the failure message;
  `started_at`/`finished_at` bracket the run.
- **`workflow_step_runs`** — per-node execution record within a run. `node_id`
  echoes the graph node; `status` mirrors the run status; `input_json` /
  `output_json` capture the data the node saw and produced (capped); `error`
  holds a per-node failure; `started_at` / `finished_at` / `duration_ms` make
  a timeline.

## Graph definition

The graph is JSON stored in `workflows.definition`:

```jsonc
{
  "nodes": [
    { "id": "start",   "type": "trigger.manual" },
    { "id": "fetch",   "type": "action.http",
      "config": { "method": "GET", "url": "https://example.com" } },
    { "id": "branch",  "type": "logic.if",
      "config": { "expr": "{{ $json.status }} eq 200" } },
    { "id": "mail",    "type": "action.mail",
      "config": { "to": "me@example.com", "subject": "ok",
                  "body": "Got {{ $json.body }}" } }
  ],
  "edges": [
    { "from": "start",  "to": "fetch",  "port": "out" },
    { "from": "fetch",  "to": "branch", "port": "out" },
    { "from": "branch", "to": "mail",   "port": "true" }
  ]
}
```

- Exactly one `trigger.*` node per graph, with no incoming edges.
- Every other node has at least one incoming edge (no orphans).
- `logic.if` emits on two named ports: `true` / `false`.
- The runtime follows edges in BFS order from the trigger; cycles are
  rejected at validation time.

## Node catalog (v1)

| Type           | Port(s)        | Behaviour                                                           |
| -------------- | -------------- | ------------------------------------------------------------------- |
| `trigger.manual`  | `out`       | Fires only when the user clicks "Run".                              |
| `trigger.webhook` | `out`       | Exposes `POST /api/workflow/hooks/{token}`; payload feeds the graph.|
| `trigger.schedule`| `out`       | Cron expression in `config.cron`; runs on the scheduler.            |
| `logic.if`        | `true,false`| Evaluates `config.expr` against the inbound JSON.                   |
| `logic.delay`     | `out`       | Sleeps `config.seconds` (≤ 300 by default) before continuing.       |
| `logic.setvar`    | `out`       | Merges `config.vars` into the context variables.                    |
| `action.http`     | `out`       | Issues an HTTP request; response JSON feeds downstream.             |
| `action.mail`     | `out`       | Sends an email via the active outbound mail driver.                 |
| `action.log`      | `out`       | Appends `config.message` (template-resolved) to the step log.       |

Templates use `{{ expr }}` references into the inbound JSON (`$json`),
trigger payload (`$trigger`), and run variables (`$vars`).

## Execution model

1. The runtime builds a node lookup and adjacency map (port → target ids)
   from the validated definition.
2. Starting from the trigger node, it walks the graph in BFS order; each
   visited node receives the upstream output as JSON.
3. Each node executes synchronously (delays use a `time.Timer`) within a
   per-run timeout (default 60s, capped to 10 minutes).
4. After every node, a `workflow_step_runs` row is written with input,
   output, status, and timing.
5. Failures short-circuit: the run is marked `failed` with the first
   node-level error; downstream nodes are skipped.

The scheduler and webhook handlers dispatch by running `Service.Run(ctx,
...)` on a background goroutine with the lifecycle context; manual runs run
inline on the request goroutine so the user sees the failure immediately.

## Module layout

`internal/workflow/` mirroring `internal/mail/` and `internal/drive/`:

```
internal/workflow/
  workflow.go          Module wiring (New, RegisterRoutes, RegisterPublicRoutes,
                       RegisterAdminRoutes, Start)
  defn/                Definition, Node, Edge types + JSON validation
  run/                 Run + StepRun domain, repository, service
  exec/                Execution engine (graph walker + node executors)
  trigger/             Scheduler + webhook registry (cron + token dispatch)
  handler/             JSON HTTP handlers (user, admin, public webhook)
```

## Endpoint inventory

```
# Public (no session)
GET  /api/workflow/status
POST /api/workflow/hooks/{token}              (webhook trigger receiver)

# Authenticated user endpoints (session required + module gate)
GET    /api/workflow/workflows?limit=&offset=
POST   /api/workflow/workflows                {name, description?, definition?}
GET    /api/workflow/workflows/{id}
PATCH  /api/workflow/workflows/{id}           {name?, description?, definition?}
DELETE /api/workflow/workflows/{id}
POST   /api/workflow/workflows/{id}/activate
POST   /api/workflow/workflows/{id}/deactivate
POST   /api/workflow/workflows/{id}/run       {input?}  (manual test run)
GET    /api/workflow/workflows/{id}/versions
GET    /api/workflow/runs?workflow=&status=&limit=&offset=
GET    /api/workflow/runs/{id}
GET    /api/workflow/runs/{id}/steps
POST   /api/workflow/runs/{id}/cancel

# Admin endpoints (admin session required) — reserved for global limits
GET  /api/admin/workflow/config
PUT  /api/admin/workflow/config               {max_run_seconds?, max_steps?}
```

## Decisions

- **JSON definition, not a separate node table.** Graphs are authored as a
  single document (the editor experience is one canvas); one column keeps
  load/save atomic and easy to snapshot.
- **BFS, not DFS.** A predictable walk order (a node fires after all its
  upstream siblings) makes branching graphs easier to reason about and to
  render in a run timeline.
- **Step rows written inline.** Each node writes its own `workflow_step_runs`
  row when it finishes, so an in-flight run is observable from the UI even
  before it completes — useful for long-running graphs.
- **Webhook token per trigger.** A random `internal/id` token is minted on
  activation; revoking deactivates the workflow. Tokens are not secret
  beyond their unguessability (single-tenant); callers can layer their own
  signatures if needed.
- **Cron via goroutine + `time.Ticker`.** A 1-second tick scans active
  schedules; the per-second overhead is negligible for a single-tenant
  engine and keeps the implementation dependency-free.

## Milestone roadmap

| #  | Milestone                                                            |
| -- | -------------------------------------------------------------------- |
| 1  | Schema `0014_workflow.sql`, Definition + Run domain, repository      |
| 2  | Service: CRUD, activate/deactivate, validate                         |
| 3  | Execution engine: graph walker + node executors + step logging       |
| 4  | Triggers: manual run, webhook receiver, cron scheduler               |
| 5  | HTTP endpoints + module wiring + status                              |
| 6  | Admin config (run/step caps) + audit logging                         |
| 7  | Tests for engine, scheduler, webhook dispatch                        |
| 8  | SPA workflow editor UI                                               |
