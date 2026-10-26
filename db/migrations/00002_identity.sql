-- +goose Up
-- The six identity tables are the explicitly approved global identity exception.
CREATE TABLE identity.users (
 id uuid PRIMARY KEY, email text NOT NULL UNIQUE CHECK(email=lower(email)), display_name text NOT NULL CHECK(length(display_name) BETWEEN 1 AND 100), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE identity.credentials (
 user_id uuid PRIMARY KEY REFERENCES identity.users(id), password_hash text NOT NULL,
 failed_attempts integer NOT NULL DEFAULT 0 CHECK(failed_attempts>=0), locked_until timestamptz,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE identity.memberships (
 id uuid PRIMARY KEY, user_id uuid NOT NULL REFERENCES identity.users(id), workspace_id uuid NOT NULL,
 role text NOT NULL CHECK(role IN ('admin','member','viewer')), created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(user_id,workspace_id)
);
CREATE INDEX membership_directory_user ON identity.memberships(user_id,workspace_id);
CREATE TABLE identity.sessions (
 id uuid PRIMARY KEY, user_id uuid REFERENCES identity.users(id), workspace_id uuid,
 kind text NOT NULL CHECK(kind IN ('anonymous','human','switch','consumer','rate')),
 token_hash bytea NOT NULL UNIQUE CHECK(octet_length(token_hash)=32), refresh_hash bytea UNIQUE,
 family_id uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL,
 refresh_expires_at timestamptz, revoked_at timestamptz, rate_count integer NOT NULL DEFAULT 0,
 CHECK(refresh_hash IS NULL OR octet_length(refresh_hash)=32),
 CHECK(kind NOT IN ('human','switch') OR (user_id IS NOT NULL AND workspace_id IS NOT NULL)),
 CHECK(kind<>'consumer' OR (user_id IS NULL AND workspace_id IS NOT NULL))
);
CREATE INDEX sessions_user ON identity.sessions(user_id,created_at);
CREATE UNIQUE INDEX consumer_workspace ON identity.sessions(workspace_id) WHERE kind='consumer';
CREATE TABLE identity.invites (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL, email text NOT NULL CHECK(email=lower(email)),
 role text NOT NULL CHECK(role IN ('member','viewer')), token_hash bytea NOT NULL UNIQUE CHECK(octet_length(token_hash)=32),
 created_by uuid NOT NULL REFERENCES identity.users(id), created_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL, used_at timestamptz
);
CREATE TABLE identity.password_resets (
 id uuid PRIMARY KEY, user_id uuid NOT NULL REFERENCES identity.users(id), token_hash bytea NOT NULL UNIQUE CHECK(octet_length(token_hash)=32),
 created_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL, used_at timestamptz
);
GRANT USAGE ON SCHEMA identity TO slipgraph_app;
GRANT SELECT,INSERT ON identity.users TO slipgraph_app;
GRANT DELETE ON identity.sessions TO slipgraph_app;
CREATE INDEX sessions_expiry ON identity.sessions(expires_at);
GRANT SELECT,INSERT,UPDATE ON identity.credentials,identity.memberships,identity.sessions,identity.invites,identity.password_resets TO slipgraph_app;

ALTER TABLE app.outbox RENAME TO events;
ALTER TABLE app.events ADD COLUMN version bigint NOT NULL DEFAULT 1;
ALTER TABLE app.events ADD COLUMN actor_kind text NOT NULL DEFAULT 'human' CHECK(actor_kind IN ('human','service','agent'));
CREATE TABLE app.workspaces (
 id uuid NOT NULL, workspace_id uuid NOT NULL CHECK(id=workspace_id), name text NOT NULL CHECK(length(name) BETWEEN 1 AND 100),
 version bigint NOT NULL DEFAULT 1, PRIMARY KEY(workspace_id,id),
 FOREIGN KEY(workspace_id,id) REFERENCES app.objects(workspace_id,id)
);
CREATE TABLE app.members (
 id uuid NOT NULL, workspace_id uuid NOT NULL, user_id uuid NOT NULL REFERENCES identity.users(id),
 role text NOT NULL CHECK(role IN ('admin','member','viewer')), display_name text NOT NULL, email text NOT NULL,
 version bigint NOT NULL DEFAULT 1, PRIMARY KEY(workspace_id,id), UNIQUE(workspace_id,user_id),
 FOREIGN KEY(workspace_id,id) REFERENCES app.objects(workspace_id,id)
);
CREATE TABLE app.delivery_receipts (
 workspace_id uuid NOT NULL, event_id uuid NOT NULL, consumer text NOT NULL, delivered_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,event_id,consumer), FOREIGN KEY(workspace_id,event_id) REFERENCES app.events(workspace_id,id)
);
CREATE TABLE app.audit_log (
 id uuid NOT NULL, workspace_id uuid NOT NULL, sequence bigint NOT NULL, event_id uuid NOT NULL,
 actor_id uuid NOT NULL, actor_kind text NOT NULL CHECK(actor_kind IN ('human','service','agent')), event_type text NOT NULL, payload jsonb NOT NULL,
 created_at timestamptz NOT NULL, previous_hash bytea NOT NULL, entry_hash bytea NOT NULL,
 PRIMARY KEY(workspace_id,id), UNIQUE(workspace_id,sequence), UNIQUE(workspace_id,event_id),
 CHECK(octet_length(previous_hash)=32 AND octet_length(entry_hash)=32)
);
-- +goose StatementBegin
DO $$
DECLARE tab text;
BEGIN
 FOREACH tab IN ARRAY ARRAY['workspaces','members','delivery_receipts','audit_log'] LOOP
  EXECUTE format('ALTER TABLE app.%I ENABLE ROW LEVEL SECURITY',tab);
  EXECUTE format('ALTER TABLE app.%I FORCE ROW LEVEL SECURITY',tab);
  EXECUTE format($policy$CREATE POLICY tenant_scope ON app.%I TO slipgraph_app,slipgraph_owner USING (workspace_id = nullif(current_setting('app.workspace_id',true),'')::uuid) WITH CHECK (workspace_id = nullif(current_setting('app.workspace_id',true),'')::uuid)$policy$,tab);
 END LOOP;
END $$;
-- +goose StatementEnd
GRANT SELECT,INSERT,UPDATE ON app.workspaces,app.members TO slipgraph_app;
GRANT SELECT,INSERT ON app.delivery_receipts TO slipgraph_app;
GRANT INSERT ON app.audit_log TO slipgraph_app;
REVOKE SELECT,UPDATE,DELETE,TRUNCATE ON app.audit_log FROM slipgraph_app;

-- Hash inputs are canonical jsonb text and an explicit protocol version.
-- +goose StatementBegin
CREATE FUNCTION app.audit_digest(p_workspace uuid,p_sequence bigint,p_event uuid,p_actor uuid,p_type text,p_kind text,p_payload jsonb,p_created timestamptz,p_previous bytea)
RETURNS bytea LANGUAGE sql IMMUTABLE SET search_path=pg_catalog,public AS $$
 SELECT public.digest(convert_to(jsonb_build_array('slipgraph-audit-v1',p_workspace,p_sequence,p_event,p_actor,p_type,p_kind,p_payload,to_char(p_created AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US'),encode(p_previous,'hex'))::text,'UTF8'),'sha256');
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION app.audit_append() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $$
DECLARE last_sequence bigint; last_hash bytea;
BEGIN
 IF NEW.workspace_id IS DISTINCT FROM nullif(current_setting('app.workspace_id',true),'')::uuid THEN RAISE EXCEPTION 'tenant required'; END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended(NEW.workspace_id::text,9401));
 IF EXISTS(SELECT 1 FROM app.audit_log WHERE workspace_id=NEW.workspace_id AND event_id=NEW.event_id) THEN RETURN NULL; END IF;
 SELECT sequence,entry_hash INTO last_sequence,last_hash FROM app.audit_log WHERE workspace_id=NEW.workspace_id ORDER BY sequence DESC LIMIT 1;
 NEW.sequence:=coalesce(last_sequence,0)+1;
 NEW.previous_hash:=coalesce(last_hash,decode(repeat('00',32),'hex'));
 NEW.entry_hash:=app.audit_digest(NEW.workspace_id,NEW.sequence,NEW.event_id,NEW.actor_id,NEW.event_type,NEW.actor_kind,NEW.payload,NEW.created_at,NEW.previous_hash);
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER audit_append_chain BEFORE INSERT ON app.audit_log FOR EACH ROW EXECUTE FUNCTION app.audit_append();
-- +goose StatementBegin
CREATE FUNCTION app.audit_immutable() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN RAISE EXCEPTION 'audit log is append-only'; END $$;
-- +goose StatementEnd
CREATE TRIGGER audit_no_mutation BEFORE UPDATE OR DELETE ON app.audit_log FOR EACH ROW EXECUTE FUNCTION app.audit_immutable();
CREATE TRIGGER audit_no_truncate BEFORE TRUNCATE ON app.audit_log FOR EACH STATEMENT EXECUTE FUNCTION app.audit_immutable();
-- +goose StatementBegin
CREATE FUNCTION app.verify_audit_chain(p_workspace uuid) RETURNS TABLE(valid boolean,entries bigint,head text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $$
DECLARE row_record record; previous bytea:=decode(repeat('00',32),'hex'); count_entries bigint:=0;
BEGIN
 IF p_workspace IS DISTINCT FROM nullif(current_setting('app.workspace_id',true),'')::uuid THEN RAISE EXCEPTION 'tenant required'; END IF;
 valid:=true;
 FOR row_record IN SELECT * FROM app.audit_log WHERE workspace_id=p_workspace ORDER BY sequence LOOP
  count_entries:=count_entries+1;
  IF row_record.sequence<>count_entries OR row_record.previous_hash<>previous OR row_record.entry_hash<>app.audit_digest(row_record.workspace_id,row_record.sequence,row_record.event_id,row_record.actor_id,row_record.event_type,row_record.actor_kind,row_record.payload,row_record.created_at,row_record.previous_hash) THEN valid:=false; END IF;
  previous:=row_record.entry_hash;
 END LOOP;
 entries:=count_entries; head:=encode(previous,'hex'); RETURN NEXT;
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION app.audit_append(),app.audit_immutable(),app.verify_audit_chain(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.verify_audit_chain(uuid) TO slipgraph_app;

-- +goose Down
DROP FUNCTION app.verify_audit_chain(uuid);
DROP TABLE app.audit_log;
DROP FUNCTION app.audit_append();
DROP FUNCTION app.audit_immutable();
DROP FUNCTION app.audit_digest(uuid,bigint,uuid,uuid,text,text,jsonb,timestamptz,bytea);
DROP TABLE app.delivery_receipts;
DROP TABLE app.members;
DROP TABLE app.workspaces;
ALTER TABLE app.events DROP COLUMN actor_kind;
ALTER TABLE app.events DROP COLUMN version;
ALTER TABLE app.events RENAME TO outbox;
DROP TABLE identity.password_resets,identity.invites,identity.sessions,identity.memberships,identity.credentials,identity.users;
