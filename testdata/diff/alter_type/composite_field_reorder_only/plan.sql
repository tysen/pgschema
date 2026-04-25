-- WARNING: composite attribute reorder requires DROP+CREATE in Postgres (no in-place reorder).
-- Function/procedure signatures referencing recreated composites are not detected; if any exist, apply will fail with a clear PG error.

DROP TYPE IF EXISTS coords RESTRICT;

CREATE TYPE coords AS (z integer, x integer, y integer);
