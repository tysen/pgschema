CREATE TABLE IF NOT EXISTS foo (
    id uuid
);

CREATE TYPE bar AS (foo_col foo);
