---
title: Claude Code history is raw transcript lines in ClickHouse, sent by Vector, readable by admins only
kind: decision
area: data
tags: [clickhouse, vector, claude-code, security, object-storage]
learned: 2026-11-22
updated: 2026-11-26
where:
  - claude-history/config/schema.sql
  - claude-history/config/users.xml
  - claude-history/route.yaml
  - infra/storage/claude-history.tf
  - secrets/claude-history.yaml
  - clusters/personal/claude-history.yaml
---

# Claude Code history is raw transcript lines in ClickHouse, sent by Vector, readable by admins only

**Decision (2026-11-22):** every line of `~/.claude/projects/**/*.jsonl`
goes, as it is, into `claude.events` (ClickHouse 26.8 LTS, one replica),
with the useful fields read from it by MATERIALIZED columns. Vector on the
laptops sends it there and to the `claude-history` bucket, which is the
backup. Only cluster admins can read it.

**Why:**

- **Raw lines, parsed in ClickHouse.** The transcript format isn't
  documented and changes between Claude Code versions (24 record types on
  2026-11-21, `api-request-blob` and `atis-latch` among them). Keeping
  `raw` means nothing is lost, and a field read wrong is fixed with `MODIFY
  COLUMN` plus `MATERIALIZE COLUMN`, not by sending everything again.
  Vector stays dumb: host, file, line.
- **Tokens per message, not per row.** Claude Code writes one assistant
  record per content block, each carrying the whole response's `usage`;
  summing rows counts a response's tokens several times.
  `claude.api_calls` takes them once per `message.id`.
- **ReplacingMergeTree on the line's hash** (`ORDER BY (session_id, ts,
  line_hash)`), so a line sent twice (retries, lost checkpoints, a restore)
  is stored once. Vector tells files apart by inode for the same reason: a
  rewritten file is read again, harmlessly.
- **ClickHouse over Postgres:** ~400 MB of transcripts a month is ~140 MB in
  ClickHouse (ZSTD), and scans, token sums and the text index stay fast
  on 50m of CPU. CloudNativePG would also have worked; this is for
  analytics as much as for search.
- **Its own Flux Kustomization** (`clusters/personal/claude-history.yaml`),
  not in infra-controllers: if ClickHouse doesn't come up, nothing else
  waits for it.

**Security:**

- The internet reaches only one request: the HTTPRoute matches Vector's
  parameterized INSERT (`query=INSERT INTO {database:Identifier}.{table:Identifier}
  FORMAT JSONEachRow`, `param_database=claude`, `param_table=events`, POST
  `/`). The real limit is the `ingest` user: INSERT on `claude.events`
  only, capped memory, a quota. DNS-only, so Cloudflare never decrypts it.
- The admin is `default` without a password, accepted only from
  127.0.0.1/::1: `kubectl exec` or `port-forward`, which only cluster
  admins can do. A password would sit in a Secret readable by the same
  people.
- **No Grafana datasource:** Grafana's Authelia client lets in every
  Authelia user (`maxcloud-developers` too) as a Viewer, and Grafana OSS
  can't keep a datasource from Viewers (no datasource permissions outside
  Enterprise), so they could query every transcript.
- Users and grants only from `users.xml`: `local_directory` (SQL-made
  users) is removed. `session_log` (logins, failures included) and
  `query_log` stay on as the audit trail.
- Vector replaces credentials of known shapes (`sk-ant-`, `ghp_`, PEM and
  age keys, JWTs, `user:password@` in URLs, ...) with `[REDACTED:<kind>]`
  before sending. Best effort: a password or Cloudflare token has no shape.
  On 2026-11-21 it caught an Anthropic key, a private key and a JWT in the
  existing transcripts.
- ClickHouse gets the ingest password's hash only (OpenBao
  `platform/claude-history/clickhouse-users`, through ESO). The password
  and the S3 writer key are at `platform/claude-history/laptop`, with no
  ExternalSecret, so they never become a Secret; laptops fetch them with
  `scripts/bao.sh`.
- The bucket's users can't delete. The reader user has an access key only
  while `-var claude_history_reader_key=true` is applied.

**Vector gotchas (0.57):**

- `${VAR}` in its config is not expanded unless
  `--dangerously-allow-env-var-interpolation` is given: a URL from an
  environment variable fails with `invalid uri character`. Credentials
  come from a `secret` backend (`type: file`) instead, and the paths are
  absolute.
- `data_dir` must exist (`data_dir ... does not exist`): the unit's
  `StateDirectory=` makes it.
- Lines over 100 KiB are dropped by default; transcripts have lines of
  1.7 MB, hence `max_line_bytes`.
- Its healthchecks do `SELECT 1` (ClickHouse) and HeadBucket (S3), which
  the route and the put-only S3 user refuse: both are turned off.

**Rejected:**

- Claude Code's OpenTelemetry export: prompts and tool events, not whole
  transcripts.
- S3Queue (ClickHouse reading the bucket itself): it processes each file
  once, but transcripts grow, and ClickHouse advises against S3
  integrations on small servers. It would also need egress.

**Revisit when:** the volume nears 10 GiB (`allowVolumeExpansion` is on); a
Vector upgrade changes the INSERT (inserts get 404: update `route.yaml`); or
someone else should read it (a read-only user, and a Grafana that only
admins can log in to).

See also: [clickhouse-small-server](clickhouse-small-server.md),
[openbao-secrets](../build-deploy/openbao-secrets.md),
[block-storage-tiers](block-storage-tiers.md).
