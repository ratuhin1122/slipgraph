#!/bin/sh
set -eu
# psql literal quoting keeps credentials out of SQL interpolation and shell tracing.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB"   --set=owner_password="$(cat /run/owner/owner_password)"   --set=app_password="$(cat /run/app/app_password)" <<'SQL'
CREATE ROLE slipgraph_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'owner_password';
CREATE ROLE slipgraph_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'app_password';
REVOKE ALL ON DATABASE slipgraph FROM PUBLIC;
GRANT CONNECT ON DATABASE slipgraph TO slipgraph_owner, slipgraph_app;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;
GRANT USAGE ON SCHEMA public TO slipgraph_owner, slipgraph_app;
CREATE SCHEMA app AUTHORIZATION slipgraph_owner;
CREATE SCHEMA migrations AUTHORIZATION slipgraph_owner;
GRANT USAGE ON SCHEMA app TO slipgraph_app;
ALTER ROLE slipgraph_owner SET timezone TO 'UTC';
ALTER ROLE slipgraph_app SET timezone TO 'UTC';
ALTER ROLE slipgraph_app SET statement_timeout TO '10s';
ALTER ROLE slipgraph_app SET idle_in_transaction_session_timeout TO '10s';
SQL
