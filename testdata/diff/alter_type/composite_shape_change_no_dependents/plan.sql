-- WARNING: composite type shape change; DROP+CREATE is the only safe path.
-- Function/procedure signatures referencing recreated composites are not detected; if any exist, apply will fail with a clear PG error.

DROP TYPE IF EXISTS team_season_detail RESTRICT;

CREATE TYPE team_season_detail AS (home_wins smallint, home_losses smallint, last10 smallint, streak smallint);
