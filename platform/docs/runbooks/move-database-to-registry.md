# Move an app's database.yaml database to the registry's `db`

Before the maxcloud controller ran databases, mcloud wrote a `database.yaml`
for apps with queue functions: a CloudNativePG Cluster `db` with **no
backups**. The registry's `db` field gives the app a Cluster `db` that backs
itself up and restores itself (README, Run platform, Databases). Both are
named `db` (so `DATABASE_URL` and the Secret `db-app` stay the same), so an
app can't have both: the data moves with a dump and a restore, and the app
is down while it does.

**Not run yet:** no app needed it when it was written (2026-11-05). Try it
on a throwaway app first, and fix this page where reality differs.

Commands are fish, run from the app's repo as one of the app's owners
(`mcloud login`) or an admin. `app` is the app's name; its database is
`app` (mcloud's default).

```fish
set app shop
set dump (mktemp -d)/$app.dump
```

## 1. Stop the workers and dump

Anything written after the dump is lost, so stop what writes: the queue
workers and plain Deployments. Knative services can't be scaled by hand
(Knative owns their Deployments); a quiet app's are idle anyway, so do this
when nobody is using it.

```fish
kubectl -n $app scale deployment -l '!serving.knative.dev/service' --replicas=0
kubectl -n $app exec db-1 -c postgres -- pg_dump -Fc -d app > $dump
ls -l $dump      # not empty
```

KEDA scales queue workers back up when jobs arrive, so check
`kubectl -n $app get deploy` stays at 0 until step 3.

## 2. Swap the databases

Remove `database.yaml` from `deploy/k8s` and from its `kustomization.yaml`,
then push: Flux deletes the old Cluster (its disk follows its
StorageClass). Then give the app the registry's database:

```fish
git rm deploy/k8s/database.yaml
# remove `- database.yaml` from deploy/k8s/kustomization.yaml
git commit -am "chore: the database comes from the registry now" && git push
kubectl -n $app get cluster db      # wait until it's gone (NotFound)
mcloud apps set $app --db
kubectl get maxcloud -n $app        # wait for the Database: Ready
```

`mcloud apps set` refuses `--db` if the app's `maxcloud.yaml` declares
`db:`. Then set `db: true` there and push (a direct app: `mcloud deploy`)
instead.

The next deploy's `functions.yaml` carries the queue's KEDA
TriggerAuthentication (it used to be in `database.yaml`): run `mcloud deploy`
or push any change, before or right after this step.

## 3. Restore the dump

```fish
kubectl -n $app exec -i db-1 -c postgres -- pg_restore -d app --no-owner --role=app < $dump
kubectl -n $app exec db-1 -c postgres -- psql -d app -c '\dt'
```

Then start what you stopped: queue workers come back by themselves (KEDA),
plain Deployments with `kubectl -n $app scale deployment <name> --replicas=1`.
Delete the dump.

The new database's first base backup is taken as soon as it's Ready;
restores from here on are the controller's (`mcloud db restore`, branches).
