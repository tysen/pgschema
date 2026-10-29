CREATE TYPE public.widget_metrics AS (
    count_a int2,
    count_b int2,
    count_x int2
);

CREATE TABLE public.widgets (
    region_id int4 NOT NULL,
    area_id int4 NOT NULL,
    detail public.widget_metrics
);

CREATE VIEW public.widget_summary AS
SELECT region_id, area_id, detail FROM public.widgets;

CREATE VIEW public.widget_overview AS
SELECT region_id, count(*) AS team_count FROM public.widget_summary GROUP BY region_id;
