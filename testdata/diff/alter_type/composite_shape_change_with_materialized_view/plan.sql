DROP MATERIALIZED VIEW season_detail_mv RESTRICT;

-- WARNING: composite type shape change; DROP+CREATE is the only safe path.
-- column public.team_season.detail data will be lost
-- Function/procedure signatures referencing recreated composites are not detected; if any exist, apply will fail with a clear PG error.

ALTER TABLE team_season DROP COLUMN detail;

DROP TYPE IF EXISTS team_season_detail RESTRICT;

CREATE TYPE team_season_detail AS (home_wins smallint, home_losses smallint, last10 smallint);

ALTER TABLE team_season ADD COLUMN detail team_season_detail;

CREATE MATERIALIZED VIEW IF NOT EXISTS season_detail_mv AS
 SELECT season_id,
    team_id,
    detail
   FROM team_season;
