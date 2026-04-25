CREATE TYPE public.location AS (
    city text,
    state text
);

CREATE TABLE public.user_profile (
    id int4 PRIMARY KEY,
    home public.location NOT NULL
);
