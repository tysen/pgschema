CREATE TYPE public.team_season_detail AS (
    home_wins int2,
    home_losses int2,
    last10_wins int2,
    last10_losses int2
);

CREATE TABLE public.team_season (
    season_id int4 NOT NULL,
    team_id int4 NOT NULL,
    detail public.team_season_detail
);

CREATE VIEW public.season_detail AS
SELECT season_id, team_id, detail FROM public.team_season;

CREATE VIEW public.season_summary AS
SELECT season_id, count(*) AS team_count FROM public.season_detail GROUP BY season_id;
