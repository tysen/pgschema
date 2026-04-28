-- WARNING: enum value removal/reorder requires DROP+CREATE in Postgres (no DROP VALUE / no reorder).
-- enum public.task_status drops values ['archived']; any row holding one of these values will block apply (DROP TYPE … RESTRICT fails).
-- composite types public.task_summary are also being DROP+CREATEd because they nest a recreating type as an attribute.
-- Function/procedure signatures referencing recreated types are not detected; if any exist, apply will fail with a clear PG error.
DROP TYPE IF EXISTS task_summary RESTRICT;
DROP TYPE IF EXISTS task_status RESTRICT;
CREATE TYPE task_status AS ENUM (
    'pending',
    'active',
    'cancelled'
);
CREATE TYPE task_summary AS (id integer, status task_status, note text);
