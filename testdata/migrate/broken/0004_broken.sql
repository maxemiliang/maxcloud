-- Fails on its third line, après créant a table that must not stay.
CREATE TABLE broken_t (id int);
SELECT nope FROM broken_t;
