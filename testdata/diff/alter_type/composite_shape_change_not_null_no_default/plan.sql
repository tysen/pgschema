-- WARNING: composite type shape change; DROP+CREATE is the only safe path.
-- column public.user_profile.home is being dropped+re-added with NOT NULL but no DEFAULT; on a non-empty table this will fail at apply. Add a DEFAULT or use a migrate-using directive if data exists.
-- Function/procedure signatures referencing recreated composites are not detected; if any exist, apply will fail with a clear PG error.

ALTER TABLE user_profile DROP COLUMN home;

DROP TYPE IF EXISTS location RESTRICT;

CREATE TYPE location AS (city text, state text, country text);

ALTER TABLE user_profile ADD COLUMN home location NOT NULL;
