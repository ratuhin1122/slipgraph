-- +goose Up
CREATE TABLE app.meetings (
 id uuid NOT NULL,workspace_id uuid NOT NULL,title text NOT NULL CHECK(length(title) BETWEEN 1 AND 200),
 storage_key text NOT NULL,media_type text NOT NULL CHECK(media_type IN ('audio/wav','audio/mpeg','audio/mp4','video/mp4','audio/webm','video/webm')),
 size_bytes bigint NOT NULL CHECK(size_bytes BETWEEN 1 AND 25000000),
 status text NOT NULL CHECK(status IN ('queued','transcribing','ready','failed','quota_exhausted')),
 duration_ms bigint NOT NULL DEFAULT 0 CHECK(duration_ms BETWEEN 0 AND 3600000),
 reserved_minutes integer NOT NULL CHECK(reserved_minutes BETWEEN 1 AND 60),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),charged_at timestamptz NOT NULL DEFAULT clock_timestamp(),lease_until timestamptz,
 PRIMARY KEY(workspace_id,id),UNIQUE(workspace_id,storage_key),FOREIGN KEY(workspace_id,id) REFERENCES app.objects(workspace_id,id),
 CHECK(storage_key LIKE workspace_id::text || '/%')
);
CREATE INDEX meeting_quota ON app.meetings(workspace_id,charged_at);
CREATE TABLE app.transcript_segments (
 id uuid NOT NULL,workspace_id uuid NOT NULL,ordinal integer NOT NULL CHECK(ordinal>=0),
 start_ms bigint NOT NULL CHECK(start_ms>=0),end_ms bigint NOT NULL CHECK(end_ms>start_ms),
 speaker text NOT NULL CHECK(length(speaker) BETWEEN 1 AND 80),text text NOT NULL CHECK(octet_length(text) BETWEEN 1 AND 4000),
 PRIMARY KEY(workspace_id,id),FOREIGN KEY(workspace_id,id) REFERENCES app.objects(workspace_id,id)
);
CREATE TABLE app.decisions (
 id uuid NOT NULL,workspace_id uuid NOT NULL,title text NOT NULL CHECK(length(title) BETWEEN 1 AND 200),details text NOT NULL CHECK(octet_length(details)<=4000),
 PRIMARY KEY(workspace_id,id),FOREIGN KEY(workspace_id,id) REFERENCES app.objects(workspace_id,id)
);
ALTER TABLE app.meetings ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.meetings FORCE ROW LEVEL SECURITY;
ALTER TABLE app.transcript_segments ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.transcript_segments FORCE ROW LEVEL SECURITY;
ALTER TABLE app.decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.decisions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON app.meetings TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
CREATE POLICY tenant_scope ON app.transcript_segments TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
CREATE POLICY tenant_scope ON app.decisions TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
GRANT SELECT,INSERT,UPDATE ON app.meetings TO slipgraph_app;
GRANT SELECT,INSERT ON app.transcript_segments,app.decisions TO slipgraph_app;
ALTER TABLE app.agent_runs DROP CONSTRAINT agent_runs_kind_check;
ALTER TABLE app.agent_runs ADD CHECK(kind IN ('reschedule_task','add_dependency','create_task','change_assignee','delay_detection','create_plan','meeting_extraction'));
ALTER TABLE app.proposals DROP CONSTRAINT proposals_kind_check;
ALTER TABLE app.proposals ADD CHECK(kind IN ('reschedule_task','add_dependency','create_task','change_assignee','create_plan','meeting_task','record_decision'));
-- +goose Down
ALTER TABLE app.agent_runs DROP CONSTRAINT agent_runs_kind_check;
ALTER TABLE app.agent_runs ADD CHECK(kind IN ('reschedule_task','add_dependency','create_task','change_assignee','delay_detection','create_plan'));
ALTER TABLE app.proposals DROP CONSTRAINT proposals_kind_check;
ALTER TABLE app.proposals ADD CHECK(kind IN ('reschedule_task','add_dependency','create_task','change_assignee','create_plan'));
DROP TABLE app.decisions;
DROP TABLE app.transcript_segments;
DROP TABLE app.meetings;
