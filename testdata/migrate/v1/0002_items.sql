CREATE TABLE items (id bigserial PRIMARY KEY, owner bigint REFERENCES users (id), name text NOT NULL);
CREATE INDEX items_owner ON items (owner);
