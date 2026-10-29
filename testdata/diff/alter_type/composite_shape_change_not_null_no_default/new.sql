CREATE TYPE public.location AS (
    city text,
    state text,
    country text
);

CREATE TABLE public.user_profile (
    id int4 PRIMARY KEY,
    home public.location NOT NULL
);
