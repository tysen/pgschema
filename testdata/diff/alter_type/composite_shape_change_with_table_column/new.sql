CREATE TYPE public.team_season_detail AS (
    home_wins int2,
    home_losses int2,
    last10 int2,
    streak int2
);

CREATE TABLE public.team_season (
    season_id int4 NOT NULL,
    team_id int4 NOT NULL,
    detail public.team_season_detail
);
