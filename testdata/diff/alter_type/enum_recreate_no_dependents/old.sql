CREATE TYPE public.task_status AS ENUM (
    'pending',
    'active',
    'queued',
    'review',
    'approved',
    'released',
    'archived',
    'cancelled'
);
