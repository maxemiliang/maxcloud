-- The first migration: a table, edited after it was applied.
CREATE TABLE users (
  id bigserial PRIMARY KEY,
  email text NOT NULL UNIQUE
);
