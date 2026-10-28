-- +goose Up
CREATE TABLE app.github_connections (
 workspace_id uuid NOT NULL,id uuid NOT NULL,requested_by uuid NOT NULL,
 connected boolean NOT NULL DEFAULT false,access_cipher bytea,route_hash text,webhook_cipher bytea,
 state_hash text,verifier_cipher bytea,oauth_session uuid,oauth_expires_at timestamptz,
 repository text NOT NULL DEFAULT '',repository_id bigint,channel_id uuid,
 stale_days integer NOT NULL DEFAULT 7 CHECK(stale_days BETWEEN 1 AND 90),
 delay_days integer NOT NULL DEFAULT 1 CHECK(delay_days BETWEEN 1 AND 365),
 PRIMARY KEY(workspace_id,id),UNIQUE(workspace_id),
 FOREIGN KEY(workspace_id,id) REFERENCES app.objects(workspace_id,id),
 FOREIGN KEY(workspace_id,channel_id) REFERENCES app.channels(workspace_id,id)
);
CREATE TABLE app.github_events (
 workspace_id uuid NOT NULL,id uuid NOT NULL,connection_id uuid NOT NULL,
 delivery_id uuid NOT NULL,body_hash text NOT NULL CHECK(length(body_hash)=64),
 event_type text NOT NULL CHECK(length(event_type)<=40),body jsonb NOT NULL,
 received_at timestamptz NOT NULL DEFAULT now(),processed_at timestamptz,
 outcome text NOT NULL DEFAULT 'queued' CHECK(outcome IN ('queued','mapped','signal','ignored')),
 PRIMARY KEY(workspace_id,id),UNIQUE(workspace_id,connection_id,delivery_id),UNIQUE(workspace_id,connection_id,body_hash),
 FOREIGN KEY(workspace_id,id) REFERENCES app.objects(workspace_id,id),
 FOREIGN KEY(workspace_id,connection_id) REFERENCES app.github_connections(workspace_id,id)
);
CREATE INDEX github_events_pending ON app.github_events(workspace_id,id) WHERE processed_at IS NULL;
CREATE TABLE app.github_items (
 workspace_id uuid NOT NULL,id uuid NOT NULL,connection_id uuid NOT NULL,
 number bigint NOT NULL CHECK(number>0),kind text NOT NULL CHECK(kind IN ('issue','pr')),
 title text NOT NULL DEFAULT '',branch text NOT NULL DEFAULT '',state text NOT NULL DEFAULT 'open',
 updated_at timestamptz NOT NULL DEFAULT now(),last_signal_at timestamptz,
 PRIMARY KEY(workspace_id,id),UNIQUE(workspace_id,connection_id,kind,number),
 FOREIGN KEY(workspace_id,id) REFERENCES app.objects(workspace_id,id),
 FOREIGN KEY(workspace_id,connection_id) REFERENCES app.github_connections(workspace_id,id)
);
ALTER TABLE app.github_connections ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.github_connections FORCE ROW LEVEL SECURITY;
ALTER TABLE app.github_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.github_events FORCE ROW LEVEL SECURITY;
ALTER TABLE app.github_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.github_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON app.github_connections TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
CREATE POLICY tenant_scope ON app.github_events TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
CREATE POLICY tenant_scope ON app.github_items TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
GRANT SELECT,INSERT,UPDATE ON app.github_connections,app.github_events,app.github_items TO slipgraph_app;
-- +goose Down
DROP TABLE app.github_items;
DROP TABLE app.github_events;
DROP TABLE app.github_connections;
