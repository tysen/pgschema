CREATE TABLE IF NOT EXISTS foo (
    id uuid
);

CREATE TYPE mid AS (f foo);

CREATE DOMAIN d AS public.mid;
