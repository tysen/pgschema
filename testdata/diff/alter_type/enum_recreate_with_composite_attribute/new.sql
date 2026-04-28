CREATE TYPE public.task_status AS ENUM (
    'pending',
    'active',
    'cancelled'
);

CREATE TYPE public.task_summary AS (
    id integer,
    status task_status,
    note text
);
