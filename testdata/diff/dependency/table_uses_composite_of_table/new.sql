CREATE TABLE public.foo (
    id uuid
);

CREATE TYPE public.mid AS (
    f foo
);

CREATE TABLE public.holder (
    m mid
);
