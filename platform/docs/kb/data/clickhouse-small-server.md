---
title: ClickHouse in a 1.5 GiB container needs a fixed memory cap, small insert blocks and fewer merge slots
kind: gotcha
area: data
tags: [clickhouse, memory, capacity]
learned: 2026-11-22
updated: 2026-11-22
where:
  - claude-history/config/server.xml
  - claude-history/config/users.xml
---

# ClickHouse in a 1.5 GiB container needs a fixed memory cap, small insert blocks and fewer merge slots

ClickHouse's defaults assume a dedicated machine with 16+ GB. Three things
broke at 1.5 GiB (26.8 LTS), each found running claude-history locally:

**Symptom 1:** inserts fail while ClickHouse itself tracks under 200 MiB:
`Code: 241. DB::Exception: (total) memory limit exceeded: would use 317.78
MiB ..., current RSS: 470.40 MiB, maximum: 472.75 MiB. OvercommitTracker
decision: Query was selected to stop by OvercommitTracker`.

**Cause:** `memory_worker_dynamic_hard_limit` (on by default) recomputes
the server's limit from the cgroup's free memory, and the page cache left
by writing parts counts as used.

**Fix:** `<memory_worker_dynamic_hard_limit>0` with a fixed
`max_server_memory_usage` (1.2 GiB) in `server.xml`. The kernel reclaims
page cache before the container's limit.

**Symptom 2:** a bulk `INSERT ... SELECT` (a restore from the bucket)
fails with the same error at 1.2 GiB, inside `JSONExtractArrayRaw` /
`multiIf`.

**Cause:** the default insert block (a million rows, 256 MB) is parsed into
every MATERIALIZED column at once.

**Fix:** `max_insert_block_size` and `min_insert_block_size_rows` 10000,
`min_insert_block_size_bytes` 32 MB in the `default` profile (`users.xml`),
which the ingest profile inherits. The whole bucket then went in at ~480
MiB; Vector's 8 MB batches take ~150 MiB. `async_insert` is off for
`ingest`: it gathers batches into bigger flushes.

**Symptom 3:** the server doesn't start: `The value of
'number_of_free_entries_in_pool_to_execute_mutation' setting (20) ... is
greater than the value of
'background_pool_size'*'background_merges_mutations_concurrency_ratio' (4)`.

**Cause:** those merge_tree thresholds default to values for a 16-thread
pool.

**Fix:** pool size 4 × ratio 2 = 8 slots, and the three
`number_of_free_entries_in_pool_to_*` settings at 4 (`<merge_tree>` in
`server.xml`).

**Recognise it:** `kubectl -n claude-history exec sts/clickhouse --
clickhouse-client -q "SELECT name, value FROM system.server_settings WHERE
name IN ('max_server_memory_usage', 'memory_worker_dynamic_hard_limit')"`,
and `MemoryTracking` in `system.metrics` against the pod's memory.

Also turned off for a small server: most system log tables (they write
every few seconds), the MySQL/PostgreSQL/interserver ports, the
uncompressed caches; the mark cache is 64 MiB instead of 5 GiB. Idle it
uses ~200 MiB.

See also: [claude-history](claude-history.md).
