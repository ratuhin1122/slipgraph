-- +goose Up
CREATE TABLE app.projects (
 id uuid NOT NULL,workspace_id uuid NOT NULL,name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 description text NOT NULL DEFAULT '' CHECK(length(description)<=4000),start_at timestamptz NOT NULL,launch_due timestamptz,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),graph_version bigint NOT NULL DEFAULT 1 CHECK(graph_version>0),
 PRIMARY KEY(workspace_id,id),FOREIGN KEY(workspace_id,id) REFERENCES app.objects(workspace_id,id),
 CHECK(launch_due IS NULL OR launch_due>=start_at)
);
CREATE TABLE app.tasks (
 id uuid NOT NULL,workspace_id uuid NOT NULL,title text NOT NULL CHECK(length(title) BETWEEN 1 AND 200),
 description text NOT NULL DEFAULT '' CHECK(length(description)<=32000),status text NOT NULL CHECK(status IN ('todo','in_progress','blocked','done','cancelled')),
 start_at timestamptz NOT NULL,due_at timestamptz NOT NULL,estimate_minutes bigint NOT NULL CHECK(estimate_minutes BETWEEN 0 AND 5256000),
 milestone boolean NOT NULL DEFAULT false,version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 reported_delay_days integer NOT NULL DEFAULT 0 CHECK(reported_delay_days BETWEEN 0 AND 3650),completed_at timestamptz,forecast_finish_at timestamptz,
 PRIMARY KEY(workspace_id,id),FOREIGN KEY(workspace_id,id) REFERENCES app.objects(workspace_id,id),
 CHECK(due_at>=start_at),CHECK(estimate_minutes>0 OR milestone),CHECK((status='done')=(completed_at IS NOT NULL))
);
-- Membership of a project and task ownership are generic links, not special-purpose FK columns.
CREATE UNIQUE INDEX links_single_parent_owner ON app.links(workspace_id,from_object,link_type) WHERE link_type IN ('belongs_to','owned_by');
CREATE TABLE app.risk_assessments (
 id uuid NOT NULL,workspace_id uuid NOT NULL,version bigint NOT NULL CHECK(version>0),
 algorithm_version text NOT NULL,source_event_id uuid NOT NULL,assessment jsonb NOT NULL,
 assessed_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(workspace_id,id),
 FOREIGN KEY(workspace_id,id) REFERENCES app.objects(workspace_id,id),
 FOREIGN KEY(workspace_id,source_event_id) REFERENCES app.events(workspace_id,id),
 UNIQUE(workspace_id,source_event_id,algorithm_version)
);
ALTER TABLE app.projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.projects FORCE ROW LEVEL SECURITY;
ALTER TABLE app.tasks ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.tasks FORCE ROW LEVEL SECURITY;
ALTER TABLE app.risk_assessments ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.risk_assessments FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON app.projects TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
CREATE POLICY tenant_scope ON app.tasks TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
CREATE POLICY tenant_scope ON app.risk_assessments TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
GRANT SELECT,INSERT,UPDATE ON app.projects,app.tasks TO slipgraph_app;
GRANT SELECT,INSERT ON app.risk_assessments TO slipgraph_app;

-- +goose Down
DROP TABLE app.risk_assessments;
DROP INDEX app.links_single_parent_owner;
DROP TABLE app.tasks;
DROP TABLE app.projects;
