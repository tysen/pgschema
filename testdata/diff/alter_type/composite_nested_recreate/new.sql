CREATE TYPE public.fan_stats AS (
    goals real,
    assists real,
    bumps real
);

CREATE TYPE public.fan_detail_season AS (
    season_id int4,
    stats fan_stats
);

CREATE TYPE public.fan_detail_game_team AS (
    team_id int4,
    detail fan_detail_season
);
