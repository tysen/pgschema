CREATE TABLE IF NOT EXISTS foo (
    id uuid
);

CREATE TYPE mid AS (f foo);

CREATE TYPE outer_t AS (m mid);
