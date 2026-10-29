DROP MATERIALIZED VIEW tasks_mv RESTRICT;
-- WARNING: enum value removal/reorder requires DROP+CREATE in Postgres (no DROP VALUE / no reorder).
-- enum public.task_status drops values ['archived']; any row holding one of these values will block apply (DROP TYPE … RESTRICT fails).
-- column public.tasks.status data will be lost
-- Function/procedure signatures referencing recreated types are not detected; if any exist, apply will fail with a clear PG error.
ALTER TABLE tasks DROP COLUMN status;
DROP TYPE IF EXISTS task_status RESTRICT;
CREATE TYPE task_status AS ENUM (
    'pending',
    'active',
    'cancelled'
);
ALTER TABLE tasks ADD COLUMN status task_status;
CREATE MATERIALIZED VIEW IF NOT EXISTS tasks_mv AS
 SELECT id,
    status
   FROM tasks;
