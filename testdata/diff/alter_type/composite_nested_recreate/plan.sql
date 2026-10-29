-- WARNING: composite type shape change; DROP+CREATE is the only safe path.
-- composite types public.region_data, public.report_data are also being DROP+CREATEd because they nest a recreating type as an attribute.
-- Function/procedure signatures referencing recreated types are not detected; if any exist, apply will fail with a clear PG error.

DROP TYPE IF EXISTS report_data RESTRICT;

DROP TYPE IF EXISTS region_data RESTRICT;

DROP TYPE IF EXISTS point_data RESTRICT;

CREATE TYPE point_data AS (score_a real, score_b real, score_c real);

CREATE TYPE region_data AS (region_id integer, stats point_data);

CREATE TYPE report_data AS (area_id integer, detail region_data);
