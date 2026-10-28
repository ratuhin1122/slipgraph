-- +goose Up
-- Defense-in-depth primitive only: no webhook HTTP endpoint is enabled.
CREATE TABLE app.webhook_receipts (
 workspace_id uuid NOT NULL,provider text NOT NULL CHECK(length(provider) BETWEEN 1 AND 100),
 delivery_id text NOT NULL CHECK(length(delivery_id) BETWEEN 1 AND 200),
 body_hash text NOT NULL CHECK(length(body_hash)=64),received_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,provider,delivery_id)
);
ALTER TABLE app.webhook_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.webhook_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON app.webhook_receipts TO slipgraph_app
 USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid)
 WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
GRANT SELECT,INSERT ON app.webhook_receipts TO slipgraph_app;
-- +goose Down
DROP TABLE app.webhook_receipts;
