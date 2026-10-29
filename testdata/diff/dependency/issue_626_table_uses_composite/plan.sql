CREATE TABLE IF NOT EXISTS foo (
    id uuid
);

CREATE TYPE mid AS (f foo);

CREATE TABLE IF NOT EXISTS holder (
    m mid
);
