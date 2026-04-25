-- WARNING: composite type shape change; DROP+CREATE is the only safe path.
-- composite types public.fan_detail_game_team, public.fan_detail_season are also being DROP+CREATEd because they nest a changing composite as an attribute.
-- Function/procedure signatures referencing recreated composites are not detected; if any exist, apply will fail with a clear PG error.
DROP TYPE IF EXISTS fan_detail_game_team RESTRICT;
DROP TYPE IF EXISTS fan_detail_season RESTRICT;
DROP TYPE IF EXISTS fan_stats RESTRICT;
CREATE TYPE fan_stats AS (goals real, assists real, bumps real);
CREATE TYPE fan_detail_season AS (season_id integer, stats fan_stats);
CREATE TYPE fan_detail_game_team AS (team_id integer, detail fan_detail_season);
