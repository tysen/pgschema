-- WARNING: composite type shape change; DROP+CREATE is the only safe path.
-- Function/procedure signatures referencing recreated types are not detected; if any exist, apply will fail with a clear PG error.

DROP TYPE IF EXISTS widget_metrics RESTRICT;

CREATE TYPE widget_metrics AS (count_a smallint, count_b smallint, count_x smallint, count_e smallint);
