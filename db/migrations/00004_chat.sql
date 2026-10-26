-- +goose Up
CREATE TABLE app.channels (
 id uuid NOT NULL, workspace_id uuid NOT NULL, name text NOT NULL CHECK(length(name) BETWEEN 1 AND 80),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 PRIMARY KEY(workspace_id,id), FOREIGN KEY(workspace_id,id) REFERENCES app.objects(workspace_id,id),
 UNIQUE(workspace_id,name)
);
CREATE TABLE app.messages (
 id uuid NOT NULL, workspace_id uuid NOT NULL, body text NOT NULL CHECK(octet_length(body)<=8000),
 thread_id uuid, client_id uuid NOT NULL, version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 PRIMARY KEY(workspace_id,id), UNIQUE(workspace_id,client_id),
 FOREIGN KEY(workspace_id,id) REFERENCES app.objects(workspace_id,id),
 FOREIGN KEY(workspace_id,thread_id) REFERENCES app.messages(workspace_id,id),
 CHECK(thread_id IS NULL OR thread_id<>id)
);
CREATE INDEX messages_thread_page ON app.messages(workspace_id,thread_id,id DESC);
CREATE INDEX links_chat_channel ON app.links(workspace_id,to_object,from_object) WHERE link_type='belongs_to';
ALTER TABLE app.channels ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.channels FORCE ROW LEVEL SECURITY;
ALTER TABLE app.messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.messages FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON app.channels TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
CREATE POLICY tenant_scope ON app.messages TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
GRANT SELECT,INSERT ON app.channels TO slipgraph_app;
GRANT SELECT,INSERT,UPDATE ON app.messages TO slipgraph_app;

-- +goose Down
DROP INDEX app.links_chat_channel;
DROP TABLE app.messages;
DROP TABLE app.channels;
