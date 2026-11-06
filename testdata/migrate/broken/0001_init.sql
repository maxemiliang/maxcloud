-- The first migration: a table.
CREATE TABLE users (
  id bigserial PRIMARY KEY,
  email text NOT NULL UNIQUE
);
