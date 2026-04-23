CREATE TABLE public.foo (
    id uuid
);

CREATE TYPE public.mid AS (
    f foo
);

CREATE TYPE public.outer_t AS (
    m mid
);
