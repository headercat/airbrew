-- 0014_workflow.sql
-- Workflow module: user-defined trigger/action automation graphs. See
-- docs/workflow.md.
--
-- Design notes:
--  * workflows.definition stores the JSON graph (nodes + edges) the editor
--    authors; version is bumped on every save so callers can detect stale
--    drafts and so we can mirror the previous definition into
--    workflow_versions for cheap history / rollback.
--  * workflow_runs + workflow_step_runs form a two-level run timeline: one
--    run row per execution, one step row per visited node. Step rows are
--    written inline as the engine visits each node so an in-flight run is
--    observable from the UI before it completes.
--  * trigger columns on workflows (webhook_token, cron_expr) are denormalised
--    from the definition so the scheduler + webhook receiver can dispatch
--    without parsing JSON on the hot path.

CREATE TABLE workflows (
  id            TEXT PRIMARY KEY NOT NULL,
  user_id       TEXT NOT NULL,
  name          TEXT NOT NULL,
  description   TEXT NOT NULL DEFAULT '',
  definition    TEXT NOT NULL DEFAULT '{"nodes":[],"edges":[]}',
  version       INTEGER NOT NULL DEFAULT 1,
  is_active     INTEGER NOT NULL DEFAULT 0,
  -- Denormalised from definition for hot-path dispatch. NULL when the graph
  -- has no matching trigger or the workflow is inactive.
  trigger_type  TEXT,                          -- manual | webhook | schedule
  webhook_token TEXT,                          -- unique URL token when webhook
  cron_expr     TEXT,                          -- 5-field cron when schedule
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_workflows_user        ON workflows(user_id, created_at DESC);
CREATE INDEX idx_workflows_active      ON workflows(is_active, trigger_type);
CREATE INDEX idx_workflows_webhook     ON workflows(webhook_token);
CREATE INDEX idx_workflows_schedule    ON workflows(is_active, cron_expr);

-- Append-only definition snapshots for history / rollback.
CREATE TABLE workflow_versions (
  id            TEXT PRIMARY KEY NOT NULL,
  workflow_id   TEXT NOT NULL,
  version       INTEGER NOT NULL,
  definition    TEXT NOT NULL,
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  FOREIGN KEY (workflow_id) REFERENCES workflows(id) ON DELETE CASCADE
);
CREATE INDEX idx_workflow_versions_wf ON workflow_versions(workflow_id, version DESC);

-- One row per execution (manual, webhook, schedule).
CREATE TABLE workflow_runs (
  id            TEXT PRIMARY KEY NOT NULL,
  workflow_id   TEXT NOT NULL,
  user_id       TEXT NOT NULL,
  version       INTEGER NOT NULL,             -- definition version that ran
  status        TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending','running','success','failed','cancelled','timed_out')),
  trigger       TEXT NOT NULL DEFAULT 'manual'
                CHECK (trigger IN ('manual','webhook','schedule')),
  -- Snapshot of trigger-time context for debugging. Capped in the service.
  input_json    TEXT NOT NULL DEFAULT '{}',
  error         TEXT NOT NULL DEFAULT '',
  started_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  finished_at   DATETIME,
  FOREIGN KEY (workflow_id) REFERENCES workflows(id) ON DELETE CASCADE,
  FOREIGN KEY (user_id)     REFERENCES users(id)     ON DELETE CASCADE
);
CREATE INDEX idx_workflow_runs_wf      ON workflow_runs(workflow_id, started_at DESC);
CREATE INDEX idx_workflow_runs_status  ON workflow_runs(status, started_at DESC);

-- One row per visited node inside a run.
CREATE TABLE workflow_step_runs (
  id            TEXT PRIMARY KEY NOT NULL,
  run_id        TEXT NOT NULL,
  node_id       TEXT NOT NULL,                -- echoes graph node id
  node_type     TEXT NOT NULL,                -- echoes graph node type
  status        TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending','running','success','failed','skipped')),
  input_json    TEXT NOT NULL DEFAULT '{}',   -- capped in service
  output_json   TEXT NOT NULL DEFAULT '{}',   -- capped in service
  error         TEXT NOT NULL DEFAULT '',
  duration_ms   INTEGER NOT NULL DEFAULT 0,
  seq           INTEGER NOT NULL,             -- visit order within the run
  started_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  finished_at   DATETIME,
  FOREIGN KEY (run_id) REFERENCES workflow_runs(id) ON DELETE CASCADE
);
CREATE INDEX idx_workflow_steps_run    ON workflow_step_runs(run_id, seq);
