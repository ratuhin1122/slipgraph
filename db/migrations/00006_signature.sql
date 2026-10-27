-- +goose Up
ALTER TABLE app.agent_runs DROP CONSTRAINT agent_runs_kind_check;
ALTER TABLE app.agent_runs ADD CHECK(kind IN ('reschedule_task','add_dependency','create_task','change_assignee','delay_detection','create_plan'));
ALTER TABLE app.agent_runs DROP CONSTRAINT agent_runs_status_check;
ALTER TABLE app.agent_runs ADD CHECK(status IN ('queued','running','completed','failed','quota_exhausted','needs_confirmation','ignored'));
ALTER TABLE app.agent_runs ADD COLUMN input jsonb NOT NULL DEFAULT '{}', ADD COLUMN result jsonb NOT NULL DEFAULT '{}', ADD COLUMN source_event_id uuid;
ALTER TABLE app.agent_runs ADD FOREIGN KEY(workspace_id,source_event_id) REFERENCES app.events(workspace_id,id);
CREATE UNIQUE INDEX agent_source_event ON app.agent_runs(workspace_id,source_event_id) WHERE source_event_id IS NOT NULL;
ALTER TABLE app.proposals DROP CONSTRAINT proposals_kind_check;
ALTER TABLE app.proposals ADD CHECK(kind IN ('reschedule_task','add_dependency','create_task','change_assignee','create_plan'));
ALTER TABLE app.proposals DROP CONSTRAINT proposals_workspace_id_created_by_agent_key;
CREATE TABLE app.briefs (
 id uuid NOT NULL,workspace_id uuid NOT NULL,body text NOT NULL CHECK(octet_length(body) BETWEEN 1 AND 8000),
 PRIMARY KEY(workspace_id,id),FOREIGN KEY(workspace_id,id) REFERENCES app.objects(workspace_id,id)
);
ALTER TABLE app.briefs ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.briefs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON app.briefs TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
GRANT SELECT,INSERT ON app.briefs TO slipgraph_app;
ALTER TABLE app.messages ADD COLUMN system_run_id uuid;
ALTER TABLE app.messages ADD FOREIGN KEY(workspace_id,system_run_id) REFERENCES app.agent_runs(workspace_id,id);
ALTER TABLE app.risk_assessments ADD COLUMN scenario_run_id uuid;
ALTER TABLE app.risk_assessments ADD FOREIGN KEY(workspace_id,scenario_run_id) REFERENCES app.agent_runs(workspace_id,id);
CREATE UNIQUE INDEX risk_scenario_run ON app.risk_assessments(workspace_id,scenario_run_id) WHERE scenario_run_id IS NOT NULL;
-- +goose Down
-- Restoring old constraints first refuses rollback if signature history exists.
ALTER TABLE app.agent_runs DROP CONSTRAINT agent_runs_kind_check;
ALTER TABLE app.agent_runs ADD CHECK(kind IN ('reschedule_task','add_dependency','create_task','change_assignee'));
ALTER TABLE app.agent_runs DROP CONSTRAINT agent_runs_status_check;
ALTER TABLE app.agent_runs ADD CHECK(status IN ('queued','running','completed','failed','quota_exhausted'));
ALTER TABLE app.proposals DROP CONSTRAINT proposals_kind_check;
ALTER TABLE app.proposals ADD CHECK(kind IN ('reschedule_task','add_dependency','create_task','change_assignee'));
ALTER TABLE app.proposals ADD UNIQUE(workspace_id,created_by_agent);
DROP INDEX app.risk_scenario_run;
ALTER TABLE app.risk_assessments DROP COLUMN scenario_run_id;
ALTER TABLE app.messages DROP COLUMN system_run_id;
DROP TABLE app.briefs;
DROP INDEX app.agent_source_event;
ALTER TABLE app.agent_runs DROP COLUMN input,DROP COLUMN result,DROP COLUMN source_event_id;
