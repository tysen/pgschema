-- WARNING: composite type shape change; DROP+CREATE is the only safe path.
-- column public.widgets.detail data will be lost
-- view public.widget_summary will be DROP+CREATEd; if its body references columns or fields removed by this change it will fail to recreate.
-- view public.widget_overview will be DROP+CREATEd; if its body references columns or fields removed by this change it will fail to recreate.
-- Function/procedure signatures referencing recreated types are not detected; if any exist, apply will fail with a clear PG error.

DROP VIEW IF EXISTS widget_overview RESTRICT;

DROP VIEW IF EXISTS widget_summary RESTRICT;

ALTER TABLE widgets DROP COLUMN detail;

DROP TYPE IF EXISTS widget_metrics RESTRICT;

CREATE TYPE widget_metrics AS (count_a smallint, count_b smallint, count_x smallint);

ALTER TABLE widgets ADD COLUMN detail widget_metrics;

CREATE OR REPLACE VIEW widget_summary AS
 SELECT region_id,
    area_id,
    detail
   FROM widgets;

CREATE OR REPLACE VIEW widget_overview AS
 SELECT region_id,
    count(*) AS team_count
   FROM widget_summary
  GROUP BY region_id;
