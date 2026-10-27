-- +goose Up
CREATE TABLE app.agent_runs (
 id uuid NOT NULL, workspace_id uuid NOT NULL, requested_by_user uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('reschedule_task','add_dependency','create_task','change_assignee')),
 status text NOT NULL CHECK(status IN ('queued','running','completed','failed','quota_exhausted')),
 steps integer NOT NULL DEFAULT 0 CHECK(steps BETWEEN 0 AND 3), tokens bigint NOT NULL DEFAULT 0 CHECK(tokens>=0),
 reserved_tokens bigint NOT NULL DEFAULT 0 CHECK(reserved_tokens>=0), cost_microusd bigint NOT NULL DEFAULT 0 CHECK(cost_microusd>=0),
 reserved_cost_microusd bigint NOT NULL DEFAULT 0 CHECK(reserved_cost_microusd>=0),
 started_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz, lease_until timestamptz,
 PRIMARY KEY(workspace_id,id), FOREIGN KEY(workspace_id,id) REFERENCES app.objects(workspace_id,id),
 FOREIGN KEY(workspace_id,requested_by_user) REFERENCES app.members(workspace_id,user_id)
);
CREATE INDEX agent_runs_quota ON app.agent_runs(workspace_id,started_at);
CREATE TABLE app.proposals (
 id uuid NOT NULL,workspace_id uuid NOT NULL,kind text NOT NULL CHECK(kind IN ('reschedule_task','add_dependency','create_task','change_assignee')),
 payload jsonb NOT NULL CHECK(octet_length(payload::text)<=32768),payload_hash text NOT NULL CHECK(length(payload_hash)=64),
 approval_hash text CHECK(length(approval_hash)=64),status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','rejected','expired','executed','failed')),
 created_by_agent uuid NOT NULL,requested_by_user uuid NOT NULL,approved_by uuid,
 expires_at timestamptz NOT NULL,executed_at timestamptz, rejection_reason text CHECK(length(rejection_reason)<=1000),
 failure_code text, before_state jsonb NOT NULL,after_state jsonb NOT NULL,version bigint NOT NULL DEFAULT 1,
 PRIMARY KEY(workspace_id,id), UNIQUE(workspace_id,created_by_agent),
 FOREIGN KEY(workspace_id,id) REFERENCES app.objects(workspace_id,id),
 FOREIGN KEY(workspace_id,created_by_agent) REFERENCES app.agent_runs(workspace_id,id),
 FOREIGN KEY(workspace_id,requested_by_user) REFERENCES app.members(workspace_id,user_id),
 FOREIGN KEY(workspace_id,approved_by) REFERENCES app.members(workspace_id,user_id),
 CHECK((status NOT IN ('approved','executed')) OR (approved_by IS NOT NULL AND approval_hash IS NOT NULL)),
 CHECK((status='executed')=(executed_at IS NOT NULL))
);
CREATE INDEX proposals_inbox ON app.proposals(workspace_id,id DESC);
ALTER TABLE app.agent_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.agent_runs FORCE ROW LEVEL SECURITY;
ALTER TABLE app.proposals ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.proposals FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON app.agent_runs TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
CREATE POLICY tenant_scope ON app.proposals TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
GRANT SELECT,INSERT,UPDATE ON app.agent_runs,app.proposals TO slipgraph_app;
-- Source object IDs are canonical generic links ('source'), exposed as source_object_ids by the API.
-- +goose Down
DROP TABLE app.proposals;
DROP TABLE app.agent_runs;
