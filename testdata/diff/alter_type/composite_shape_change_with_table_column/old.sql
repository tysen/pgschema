CREATE TYPE public.widget_metrics AS (
    count_a int2,
    count_b int2,
    count_c int2,
    count_d int2,
    count_e int2
);

CREATE TABLE public.widgets (
    region_id int4 NOT NULL,
    area_id int4 NOT NULL,
    detail public.widget_metrics
);
