-- +goose Up
CREATE TABLE app.embeddings (
 workspace_id uuid NOT NULL,object_id uuid NOT NULL,chunk_index integer NOT NULL CHECK(chunk_index>=0),
 chunk text NOT NULL CHECK(octet_length(chunk) BETWEEN 1 AND 2400),vector public.vector(384) NOT NULL,
 model text NOT NULL CHECK(length(model) BETWEEN 1 AND 100),content_hash text NOT NULL,
 document tsvector GENERATED ALWAYS AS (to_tsvector('english',chunk)) STORED,
 PRIMARY KEY(workspace_id,object_id,chunk_index),
 FOREIGN KEY(workspace_id,object_id) REFERENCES app.objects(workspace_id,id)
);
CREATE INDEX embeddings_hnsw ON app.embeddings USING hnsw(vector public.vector_cosine_ops);
CREATE INDEX embeddings_fts ON app.embeddings USING gin(document);
CREATE TABLE app.search_index_jobs (
 workspace_id uuid NOT NULL,object_id uuid NOT NULL,generation bigint NOT NULL DEFAULT 1,
 processed_generation bigint NOT NULL DEFAULT 0,lease_until timestamptz,retry_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,object_id),FOREIGN KEY(workspace_id,object_id) REFERENCES app.objects(workspace_id,id)
);
CREATE INDEX search_index_pending ON app.search_index_jobs(workspace_id,retry_at,object_id) WHERE generation>processed_generation;
CREATE TABLE app.search_requests (
 workspace_id uuid NOT NULL,id uuid NOT NULL,requested_by uuid NOT NULL,query text NOT NULL CHECK(octet_length(query) BETWEEN 1 AND 500),
 kind text NOT NULL CHECK(kind IN ('search','ask')),status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','complete','failed')),
 result jsonb,created_at timestamptz NOT NULL DEFAULT now(),lease_until timestamptz,
 PRIMARY KEY(workspace_id,id),FOREIGN KEY(workspace_id,id) REFERENCES app.objects(workspace_id,id),
 FOREIGN KEY(workspace_id,requested_by) REFERENCES app.members(workspace_id,user_id)
);
CREATE INDEX search_requests_pending ON app.search_requests(workspace_id,created_at) WHERE status='queued';
CREATE TABLE app.search_usage (
 workspace_id uuid NOT NULL,id uuid NOT NULL,tokens bigint NOT NULL CHECK(tokens>0),cost_microusd bigint NOT NULL CHECK(cost_microusd>0),created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,id)
);
CREATE INDEX search_usage_budget ON app.search_usage(workspace_id,created_at);
ALTER TABLE app.embeddings ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.embeddings FORCE ROW LEVEL SECURITY;
ALTER TABLE app.search_index_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.search_index_jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE app.search_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.search_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE app.search_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.search_usage FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON app.embeddings TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
CREATE POLICY tenant_scope ON app.search_index_jobs TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
CREATE POLICY tenant_scope ON app.search_requests TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
CREATE POLICY tenant_scope ON app.search_usage TO slipgraph_app USING(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK(workspace_id=nullif(current_setting('app.workspace_id',true),'')::uuid);
GRANT SELECT,INSERT,UPDATE,DELETE ON app.embeddings TO slipgraph_app;
GRANT SELECT,INSERT,UPDATE ON app.search_index_jobs,app.search_requests TO slipgraph_app;
GRANT SELECT,INSERT ON app.search_usage TO slipgraph_app;
-- Security-invoker preserves FORCE RLS on every underlying table.
CREATE VIEW app.search_documents WITH (security_invoker=true) AS
 SELECT o.workspace_id,o.id,'task'::text AS type,t.title,t.title || chr(10) || t.description AS content,
 '/projects/' || p.id::text || '?task=' || t.id::text AS url,
 encode(public.digest(t.title || chr(10) || t.description,'sha256'),'hex') AS content_hash
 FROM app.objects o JOIN app.tasks t ON (t.workspace_id,t.id)=(o.workspace_id,o.id)
 JOIN app.links l ON l.workspace_id=o.workspace_id AND l.from_object=o.id AND l.link_type='belongs_to'
 JOIN app.objects p ON p.workspace_id=o.workspace_id AND p.id=l.to_object AND p.type='project' AND p.deleted_at IS NULL
 WHERE o.type='task' AND o.deleted_at IS NULL
 UNION ALL
 SELECT o.workspace_id,o.id,'message',left(m.body,100),m.body,
 '/chat/' || c.id::text || '?message=' || m.id::text,
 encode(public.digest(m.body,'sha256'),'hex')
 FROM app.objects o JOIN app.messages m ON (m.workspace_id,m.id)=(o.workspace_id,o.id)
 JOIN app.links l ON l.workspace_id=o.workspace_id AND l.from_object=o.id AND l.link_type='belongs_to'
 JOIN app.objects c ON c.workspace_id=o.workspace_id AND c.id=l.to_object AND c.type='channel' AND c.deleted_at IS NULL
 WHERE o.type='message' AND o.deleted_at IS NULL AND length(m.body)>0
 UNION ALL
 SELECT o.workspace_id,o.id,'transcript_segment',s.speaker,s.text,
 '/meetings/' || m.id::text || '?segment=' || s.id::text || '&t=' || (s.start_ms/1000)::text,
 encode(public.digest(s.text,'sha256'),'hex')
 FROM app.objects o JOIN app.transcript_segments s ON (s.workspace_id,s.id)=(o.workspace_id,o.id)
 JOIN app.links l ON l.workspace_id=o.workspace_id AND l.from_object=o.id AND l.link_type='belongs_to'
 JOIN app.objects m ON m.workspace_id=o.workspace_id AND m.id=l.to_object AND m.type='meeting' AND m.deleted_at IS NULL
 JOIN app.links ml ON ml.workspace_id=m.workspace_id AND ml.from_object=m.id AND ml.link_type='belongs_to'
 JOIN app.objects p ON p.workspace_id=m.workspace_id AND p.id=ml.to_object AND p.type='project' AND p.deleted_at IS NULL
 WHERE o.type='transcript_segment' AND o.deleted_at IS NULL;
GRANT SELECT ON app.search_documents TO slipgraph_app;
-- Existing content is backfilled by authorized tenant worker sessions (never RLS-bypassed owner reads).
-- +goose Down
DROP VIEW app.search_documents;
DROP TABLE app.search_usage;
DROP TABLE app.search_requests;
DROP TABLE app.search_index_jobs;
DROP TABLE app.embeddings;
