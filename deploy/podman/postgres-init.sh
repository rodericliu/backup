#!/bin/sh
# Run once by the postgres image on an empty data directory
# (/docker-entrypoint-initdb.d): creates the backup role, database and schema.
# The password of the backup role is taken from BACKUP_DB_PASSWORD.
set -eu

: "${BACKUP_DB_PASSWORD:?BACKUP_DB_PASSWORD is not set}"

psql -v ON_ERROR_STOP=1 -v pw="$BACKUP_DB_PASSWORD" --username "$POSTGRES_USER" --dbname postgres <<'SQL'
CREATE ROLE backup WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE CONNECTION LIMIT 30 PASSWORD :'pw';
CREATE DATABASE backup WITH OWNER = backup ENCODING = 'UTF8' TEMPLATE = template0;
REVOKE ALL ON DATABASE backup FROM PUBLIC;
GRANT CONNECT, TEMPORARY ON DATABASE backup TO backup;
SQL

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname backup <<'SQL'
CREATE SCHEMA backup AUTHORIZATION backup;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
ALTER ROLE backup IN DATABASE backup SET search_path = backup;
ALTER ROLE backup IN DATABASE backup SET timezone = 'UTC';
SQL
