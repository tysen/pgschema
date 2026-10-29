CREATE TYPE public.task_status AS ENUM (
    'pending',
    'active',
    'review',
    'approved',
    'released',
    'archived',
    'cancelled',
    'restored'
);
