# Restore a Postgres cluster from backup

Restores a CloudNativePG cluster's base backup plus archived WAL from
`s3://pg-backups/<cluster>/` into a **new** cluster `<cluster>-restore`, to any
point in time inside the retention window (7 days). The source cluster is not
touched, and the restore never writes to the bucket.

For a run platform app's database (`db: true`), use `mcloud db restore <app>
--at <time>` instead: the maxcloud controller restores it from the app's own
bucket. This runbook is for a CloudNativePG cluster you run yourself that
archives to `pg-backups`.

Commands are fish. `ns` is the namespace with the source cluster and its
`ObjectStore` (`pg-backups`) and `backup-credentials` Secret, `db` the database
inside it (`app` unless the cluster's `bootstrap.initdb.database` says otherwise).

```fish
set ns <namespace>
set src <cluster>
set db app
```

The restore runs next to the source and never touches it, so a drill against
production is safe. The marker rows below are written to a separate
`restore_check` table; drop it afterwards.

## 1. Pick the target time

Either a time you want to go back to, or (for a drill) a marker you write now:

```fish
set primary (kubectl -n $ns get cluster $src -o jsonpath='{.status.currentPrimary}')
function sql --description 'run SQL on a CNPG instance: sql <pod> <query>'
    kubectl -n $ns exec $argv[1] -c postgres -- psql -d $db -Atc $argv[2]
end

sql $primary "create table if not exists restore_check (id serial primary key, note text, at timestamptz default now())"
sql $primary "insert into restore_check (note) select 'before target' from generate_series(1, 1000)"
set target (sql $primary "select now()")
echo $target
sleep 2
sql $primary "insert into restore_check (note) select 'after target' from generate_series(1, 500)"
# Close the current WAL segment so it's archived now, not at the 5 min timeout.
sql $primary "select pg_switch_wal()"
```

Recovery needs archived WAL **past** the target (it stops at the first commit
after it), which is why there's an insert after the marker and a WAL switch.
Check that the segment was archived:

```fish
sql $primary "select last_archived_wal, last_archived_time, failed_count from pg_stat_archiver"
```

## 2. Restore

```fish
sed -e "s/SOURCE_CLUSTER/$src/g" -e "s/TARGET_TIME/$target/" docs/runbooks/pg-restore.yaml | kubectl -n $ns apply -f -
kubectl -n $ns get cluster $src-restore -w     # "Cluster in healthy state", 1-3 min
```

If it stalls, the restore job's log says why:

```fish
kubectl -n $ns logs -l cnpg.io/cluster=$src-restore --all-containers --tail=50
```

## 3. Compare

```fish
sql $primary "select note, count(*) from restore_check group by note order by note"
sql $src-restore-1 "select note, count(*) from restore_check group by note order by note"
```

The source has both groups; the restore must have **only** `before target|1000`.

## 4. Clean up

The restore cluster uses the `upcloud-block-storage-maxiops-encrypted-delete`
StorageClass, so deleting it also deletes its PVC, PersistentVolume and
UpCloud disk.

```fish
kubectl -n $ns delete cluster $src-restore
kubectl get pv | grep $src-restore      # gone within a few seconds
```

For a drill, also drop the marker table:
`sql $primary "drop table restore_check"`.
