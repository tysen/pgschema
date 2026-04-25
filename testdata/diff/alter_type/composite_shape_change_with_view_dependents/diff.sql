-- WARNING: composite type shape change; DROP+CREATE is the only safe path.
-- column public.team_season.detail data will be lost
-- view public.season_detail will be DROP+CREATEd; any view body that destructures composite fields removed in this change will fail to recreate.
-- view public.season_summary will be DROP+CREATEd; any view body that destructures composite fields removed in this change will fail to recreate.
-- Function/procedure signatures referencing recreated composites are not detected; if any exist, apply will fail with a clear PG error.
DROP VIEW IF EXISTS season_summary RESTRICT;
DROP VIEW IF EXISTS season_detail RESTRICT;
ALTER TABLE team_season DROP COLUMN detail;
DROP TYPE IF EXISTS team_season_detail RESTRICT;
CREATE TYPE team_season_detail AS (home_wins smallint, home_losses smallint, last10 smallint);
ALTER TABLE team_season ADD COLUMN detail team_season_detail;
CREATE OR REPLACE VIEW season_detail AS
 SELECT season_id,
    team_id,
    detail
   FROM team_season;
CREATE OR REPLACE VIEW season_summary AS
 SELECT season_id,
    count(*) AS team_count
   FROM season_detail
  GROUP BY season_id;
