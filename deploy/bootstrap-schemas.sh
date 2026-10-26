#!/bin/sh
set -eu
export PGPASSWORD="$(cat /run/postgres/postgres_password)"
psql -v ON_ERROR_STOP=1 -h postgres -U postgres -d slipgraph <<'SQL'
CREATE SCHEMA IF NOT EXISTS identity AUTHORIZATION slipgraph_owner;
CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;
REVOKE ALL ON SCHEMA identity FROM PUBLIC;
SQL
