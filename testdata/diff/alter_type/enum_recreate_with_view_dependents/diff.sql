-- WARNING: enum value removal/reorder requires DROP+CREATE in Postgres (no DROP VALUE / no reorder).
-- enum public.task_status drops values ['archived']; any row holding one of these values will block apply (DROP TYPE … RESTRICT fails).
-- column public.tasks.status data will be lost
-- view public.tasks_view will be DROP+CREATEd; if its body references columns or fields removed by this change it will fail to recreate.
-- Function/procedure signatures referencing recreated types are not detected; if any exist, apply will fail with a clear PG error.
DROP VIEW IF EXISTS tasks_view RESTRICT;
ALTER TABLE tasks DROP COLUMN status;
DROP TYPE IF EXISTS task_status RESTRICT;
CREATE TYPE task_status AS ENUM (
    'pending',
    'active',
    'cancelled'
);
ALTER TABLE tasks ADD COLUMN status task_status;
CREATE OR REPLACE VIEW tasks_view AS
 SELECT id,
    status
   FROM tasks;
