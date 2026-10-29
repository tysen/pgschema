-- WARNING: enum value removal/reorder requires DROP+CREATE in Postgres (no DROP VALUE / no reorder).
-- enum public.task_status drops values ['queued']; any row holding one of these values will block apply (DROP TYPE … RESTRICT fails).
-- Function/procedure signatures referencing recreated types are not detected; if any exist, apply will fail with a clear PG error.
DROP TYPE IF EXISTS task_status RESTRICT;
CREATE TYPE task_status AS ENUM (
    'pending',
    'active',
    'review',
    'approved',
    'released',
    'archived',
    'cancelled',
    'restored'
);
