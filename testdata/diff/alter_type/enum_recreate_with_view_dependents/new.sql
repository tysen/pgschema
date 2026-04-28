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

CREATE VIEW public.tasks_view AS
SELECT id, status FROM public.tasks;
