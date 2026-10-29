CREATE TABLE public.foo (
    id uuid
);

CREATE TYPE public.mid AS (
    f foo
);

CREATE DOMAIN public.d AS mid;
