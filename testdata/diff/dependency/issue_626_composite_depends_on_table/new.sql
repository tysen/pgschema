CREATE TABLE public.foo (
    id uuid
);

CREATE TYPE public.bar AS (
    foo_col foo
);
