---
title: UpCloud sub-account usernames can't contain dashes; Object Storage users can
kind: gotcha
area: ops
tags: [upcloud, upctl, sub-account, api-token, object-storage, iam]
learned: 2026-11-05
updated: 2026-11-08
where:
  - infra/bootstrap/README.md
  - infra/storage/maxcloud.tf
  - infrastructure/controllers/maxcloud/kustomization.yaml
---

# UpCloud sub-account usernames can't contain dashes; Object Storage users can

**Symptom:** `upctl account create --username maxcloud-controller ...` is
refused; the username can't have a `-`.

**Cause:** UpCloud has two kinds of "user" with different name rules:

- **Account and sub-account usernames** (API and Hub logins, what tokens
  belong to) don't allow dashes. Use underscores.
- **Managed Object Storage users** (S3 access keys inside one Object
  Storage instance) do allow dashes.

**What we have:**

| Kind | Name |
| --- | --- |
| Sub-account for OpenTofu and upctl | `homelab_api` |
| Sub-account for the maxcloud controller (only the `maxcloud-apps` instance) | `maxcloud_controller` |
| Object Storage users | `tofu-state`, `cnpg-backups`, `loki`, `maxcloud-controller`, `zot`, and one `mc-<app>` per app (made by the controller) |

So the controller's sub-account is `maxcloud_controller`, and its S3 user
in `infra/storage/maxcloud.tf` is `maxcloud-controller`. These are two
different things.

**Recognise it:** `upctl account show` prints the sub-account behind the
current credentials. An older README said the first sub-account was
`homelab-tofu`, but it is really `homelab_api`.

See also: [node-autoscaling-burst-pool](node-autoscaling-burst-pool.md),
whose planned sub-account must follow the same rule.
