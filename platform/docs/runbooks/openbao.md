# OpenBao: set up, migrate from SOPS, rebuild, repair

OpenBao holds every platform and app secret, and External Secrets Operator
(ESO) turns them into Secrets (README, "Secrets"; why:
[openbao-secrets](../kb/build-deploy/openbao-secrets.md)). Commands are fish,
from `platform/`.

What matters most is the seal key, `~/.config/openbao/seal.key`, with its
copy in the password manager. OpenBao's database and its backups are
unreadable without it.

## 1. Migrate from SOPS (once, 2026-11)

Done on 2026-11-26 and kept as a record. `scripts/sops-to-bao.sh` was
deleted afterwards; git history still has it.

Before this, the platform's Secrets came from SOPS files in the private
repo (Flux Kustomization `secrets`), and apps' from `app-secrets` plus the
controller's age copies. The change ships as separate commits, deployed in
this order. Nothing is down at any step: the old Secrets stay until ESO
has taken each one over.

1. **tofu: OpenBao's bootstrap Secrets.**

   ```fish
   openssl rand -out ~/.config/openbao/seal.key 32   # then into the password manager
   tofu -chdir=infra/storage apply   # the openbao-backups S3 user
   tofu -chdir=infra/flux apply      # namespace openbao, Secrets openbao-seal and openbao-backup-s3
   ```

   Apply the commit that adds `infra/flux/openbao.tf`, not the later one
   that removes `sops-age` (step 5).

2. **OpenBao and ESO** (commit "OpenBao, ESO and trust-manager"): merge,
   then wait for:

   ```fish
   flux get kustomization infra-controllers secrets-store
   kubectl -n openbao get pods,cluster,job
   kubectl -n openbao logs job/openbao-config | tail -3   # "OpenBao configured"
   ```

   Nothing reads from OpenBao yet. If `openbao-0` loops with `failed to
   initialize`, see section 4.

3. **Copy the platform's values in and switch** (commit "platform secrets
   from OpenBao"). With the old SOPS key and `mcloud login` as an admin:

   ```fish
   scripts/sops-to-bao.sh --dry-run
   scripts/sops-to-bao.sh
   ```

   It writes each file to `platform/<namespace>/<name>` (never over an
   existing value) and marks the Secret `prune: disabled`, so the old
   Kustomization doesn't delete it when it stops listing it
   ([eso-takeover-of-existing-secrets](../kb/build-deploy/eso-takeover-of-existing-secrets.md)).
   Then merge the commit. It points `secrets` at `platform/secrets` and
   drops every `decryption:` block. Check:

   ```fish
   flux get kustomization secrets
   kubectl get externalsecret -A    # all SecretSynced
   ```

   Then restart what reads Secrets only at start, so a mistake shows up
   now rather than at the next reboot: `kubectl -n authelia rollout
   restart deploy/authelia`, the same for `external-dns`, `registry/zot`,
   `maxcloud-system/maxcloud-controller`, and Grafana and Alertmanager in
   `monitoring`.

4. **Apps' secrets** (commit "apps' secrets in OpenBao", which needs a
   controller release with OpenBao support): bump the controller's tag in
   `clusters/personal/maxcloud.yaml` in the same commit. On start the
   controller copies every `app-secrets` into `apps/<namespace>/app-secrets`
   and writes an empty one for apps without any. The registry's
   ExternalSecret then takes the Secret over. Raise
   `MAXCLOUD_MIN_MCLOUD_VERSION` to the mcloud release that talks to the
   API: an older mcloud still edits the Secret directly, and ESO puts the
   old value back on its next sync.

   ```fish
   kubectl get externalsecret -A -l app.kubernetes.io/part-of=maxcloud
   mcloud secrets list --app console
   ```

5. **Clean up** (commit "drop SOPS"): it removes `sops-age` from
   `infra/flux`, and `sops` and `age` from `mise.toml`.
   `tofu -chdir=infra/flux apply`. In the private repo, delete `secrets/`
   and `.sops.yaml`. The controller's age copies in `maxcloud-registry`
   (`secrets/*.age`) can go too, and so can `MAXCLOUD_SECRETS_AGE_KEY` from
   `platform/maxcloud-system/maxcloud-controller`:

   ```fish
   scripts/bao.sh kv patch -mount=platform maxcloud-system/maxcloud-controller MAXCLOUD_SECRETS_AGE_KEY=
   ```

   Keep the old age key in the password manager until the values it
   encrypted (still in the private repo's history) have been rotated.

## 2. A brand-new platform (no backup yet)

As above, but there is nothing to migrate and no admin login yet (Authelia
needs its secrets first):

1. Make the seal key, then `tofu apply` `infra/storage` and `infra/flux`.
2. Flux brings OpenBao up (`secrets-store`). Write each platform secret
   with the break-glass login, which needs only `kubectl`. Authelia's come
   from the generator:

   ```fish
   BAO_BREAK_GLASS=1 scripts/authelia-secrets.sh
   printf %s $cloudflare_token | scripts/bao.sh --break-glass kv put -mount=platform cert-manager/cloudflare-api-token api-token=-
   # ... one per ExternalSecret in secrets/
   ```

3. Once the first base backup exists (`kubectl -n openbao get backup`),
   turn on restoring (section 3).

## 3. Restoring OpenBao after a rebuild

Make a rebuilt cluster restore OpenBao from its backup instead of starting
empty. Do it once, after the first backup:

1. In `secrets-store/database.yaml`, replace `bootstrap.initdb` with the
   commented-out `bootstrap.recovery` and `externalClusters`. Change the
   plugin's `serverName` to `openbao-db-2`, so the restored cluster
   archives next to the backups it restores from, not over them. Merge.
   The running Cluster ignores `bootstrap`; only a new one uses it.
2. On a rebuild: `tofu apply` `infra/flux` with the **same** seal key. Flux
   brings up `openbao-db` from the backup, and OpenBao unseals and serves
   the old data. Self-init doesn't run (the storage isn't empty), and the
   config Job applies `config/` again.
3. After the restore, bump `serverName` once more (`openbao-db-3`) and
   point `externalClusters` at the one just restored from, so the next
   rebuild restores the newest data.

Check before you need it: `docs/runbooks/postgres-restore.md` restores the
same backup into `openbao-db-restore` next to the running one.

## 4. Repairs

- **`failed to initialize` on first start:** self-init failed, so the root
  token is gone and nobody can log in
  ([openbao-self-init-and-audit](../kb/build-deploy/openbao-self-init-and-audit.md)).
  Only while the storage holds no secrets yet: fix the config, then
  `kubectl -n openbao delete cluster openbao-db` (its PVC goes with it).
  Flux makes a new one, and the next start runs self-init on empty
  storage. With data in it, never do this; restore from backup instead.
- **HelmRelease `openbao` Stalled (`RetriesExceeded`):** an install that
  gave up while the database was starting. `RetryOnFailure` should prevent
  it; to retry at once: `flux reconcile helmrelease openbao -n openbao
  --reset`.
- **Sealed (`OpenBaoSealed`):** the key in Secret `openbao-seal` doesn't
  match the one the data was sealed with. Put the right file back
  (`tofu -chdir=infra/flux apply -var openbao_seal_key_revision=<n+1>`) and
  delete the pod.
- **Authelia is down, or the oidc login fails:** `scripts/bao.sh
  --break-glass ...`. It's a 10-minute ServiceAccount token, and only
  cluster admins can mint one.
- **An ExternalSecret isn't syncing (`ExternalSecretNotSynced`):**
  `kubectl -n <ns> describe externalsecret <name>`. Usually the path is
  missing in OpenBao (`scripts/bao.sh kv get -mount=platform <ns>/<name>`)
  or OpenBao is down. The Secret keeps its last values meanwhile.
- **Rotating the seal key:** the static seal takes `current_key` plus
  `previous_key` (with their ids). Put both keys in `openbao-seal`, set
  `previous_key`/`previous_key_id` and the new `current_key_id` in
  `secrets-store/release.yaml`, roll the pod, then drop the old key. Rehearse
  in kind first; it isn't routine.
