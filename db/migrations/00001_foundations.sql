-- +goose Up
CREATE TABLE app.objects (
    id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    type text NOT NULL CHECK (length(type) BETWEEN 1 AND 64),
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    PRIMARY KEY (workspace_id, id)
);
CREATE TABLE app.links (
    id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    from_object uuid NOT NULL,
    to_object uuid NOT NULL,
    link_type text NOT NULL CHECK (length(link_type) BETWEEN 1 AND 64),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, id),
    UNIQUE (workspace_id, from_object, to_object, link_type),
    FOREIGN KEY (workspace_id, from_object) REFERENCES app.objects(workspace_id, id),
    FOREIGN KEY (workspace_id, to_object) REFERENCES app.objects(workspace_id, id)
);
CREATE INDEX links_reverse ON app.links(workspace_id, to_object, link_type);
CREATE TABLE app.outbox (
    id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    object_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    PRIMARY KEY (workspace_id, id),
    FOREIGN KEY (workspace_id, object_id) REFERENCES app.objects(workspace_id, id)
);
CREATE INDEX outbox_pending ON app.outbox(workspace_id, created_at) WHERE published_at IS NULL;
ALTER TABLE app.objects ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.objects FORCE ROW LEVEL SECURITY;
ALTER TABLE app.links ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.links FORCE ROW LEVEL SECURITY;
ALTER TABLE app.outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_objects ON app.objects FOR ALL TO slipgraph_app
    USING (workspace_id = nullif(current_setting('app.workspace_id', true), '')::uuid)
    WITH CHECK (workspace_id = nullif(current_setting('app.workspace_id', true), '')::uuid);
CREATE POLICY tenant_links ON app.links FOR ALL TO slipgraph_app
    USING (workspace_id = nullif(current_setting('app.workspace_id', true), '')::uuid)
    WITH CHECK (workspace_id = nullif(current_setting('app.workspace_id', true), '')::uuid);
CREATE POLICY tenant_outbox ON app.outbox FOR ALL TO slipgraph_app
    USING (workspace_id = nullif(current_setting('app.workspace_id', true), '')::uuid)
    WITH CHECK (workspace_id = nullif(current_setting('app.workspace_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON app.objects, app.links TO slipgraph_app;
GRANT SELECT, INSERT ON app.outbox TO slipgraph_app;
GRANT UPDATE (published_at) ON app.outbox TO slipgraph_app;

-- +goose Down
DROP TABLE app.outbox;
DROP TABLE app.links;
DROP TABLE app.objects;
