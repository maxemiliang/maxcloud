# Restore claude-history

claude-history's ClickHouse (`claude-history/`) has one volume and no
backups of its own. Every line it holds is also somewhere else:

- **on the laptops**, in `~/.claude/projects/` (kept 10 years:
  `cleanupPeriodDays` in `~/.claude/settings.json`);
- **in the `claude-history` bucket** (`raw/<host>/<date>/*.log.zst`), which
  Vector writes next to every insert and nothing deletes from.

So a restore is sending them again. Lines that arrive twice are merged into
one (the table is a ReplacingMergeTree on the line's hash), so it doesn't
matter if some were already there.

Commands are fish.

## 1. An empty ClickHouse

If the volume is gone (PVC deleted, cluster rebuilt), Flux recreates the
StatefulSet and its volume, and `schema.sql` recreates the tables. Check it's
up:

```fish
kubectl -n claude-history get pods
kubectl -n claude-history exec sts/clickhouse -- clickhouse-client -q 'SELECT count() FROM claude.events'
```

## 2. From the laptops

On each laptop, make Vector forget how far it had read, so it sends every
transcript still on disk again:

```fish
systemctl --user stop claude-history
rm -r ~/.local/state/claude-history/transcripts
systemctl --user start claude-history
journalctl --user -u claude-history -f
```

The bucket gets these lines a second time too; that's harmless (restoring
from it dedupes the same way).

## 3. From the bucket

For what is no longer on any laptop. The `claude-history-reader` S3 user can
list and read the bucket, but only has an access key while you want one:

```fish
cd platform
tofu -chdir=infra/storage apply -var claude_history_reader_key=true
set -l key (tofu -chdir=infra/storage output -raw claude_history_reader_access_key_id)
set -l secret (tofu -chdir=infra/storage output -raw claude_history_reader_secret_access_key)
```

ClickHouse in the cluster can't reach Object Storage (its NetworkPolicy
allows no egress), so the laptop reads the bucket with `clickhouse local` and
streams the rows into the cluster's ClickHouse through a port-forward, as the
admin (`default`, which only accepts connections from the pod's localhost):

```fish
kubectl -n claude-history port-forward sts/clickhouse 18123:8123 &
docker run --rm --network host clickhouse/clickhouse-server:26.8.43 clickhouse local -q "
  SELECT host, file, raw
  FROM s3('https://q6c58.upcloudobjects.com/claude-history/raw/**/*.log.zst', '$key', '$secret', 'JSONEachRow')
  FORMAT JSONEachRow" |
  curl -sS --fail-with-body --data-binary @- \
    'http://127.0.0.1:18123/?query=INSERT%20INTO%20claude.events%20(host,file,raw)%20FORMAT%20JSONEachRow'
kill %1
```

`raw/<host>/` or `raw/<host>/2026/11/` instead of `raw/**` restores one
laptop or one month. Then delete the key again:

```fish
tofu -chdir=infra/storage apply
```

## 4. Check

```fish
kubectl -n claude-history exec sts/clickhouse -- clickhouse-client -q "
  SELECT host, count(), uniqExact(session_id), min(ts), max(ts)
  FROM claude.events FINAL WHERE ts > 0 GROUP BY host"
```
