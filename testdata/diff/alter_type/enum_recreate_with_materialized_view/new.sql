CREATE TYPE public.task_status AS ENUM (
    'pending',
    'active',
    'cancelled'
);

CREATE TABLE public.tasks (
    id integer PRIMARY KEY,
    status task_status,
    note text
);

CREATE MATERIALIZED VIEW public.tasks_mv AS
SELECT id, status FROM public.tasks;
