CREATE TYPE public.task_status AS ENUM (
    'pending',
    'active',
    'archived'
);

CREATE DOMAIN public.task_status_d AS task_status NOT NULL;
