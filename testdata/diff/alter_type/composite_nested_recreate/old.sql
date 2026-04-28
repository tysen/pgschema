CREATE TYPE public.point_data AS (
    score_a real,
    score_b real
);

CREATE TYPE public.region_data AS (
    region_id int4,
    stats point_data
);

CREATE TYPE public.report_data AS (
    area_id int4,
    detail region_data
);
