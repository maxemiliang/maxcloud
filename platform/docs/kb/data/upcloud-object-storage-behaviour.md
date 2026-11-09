---
title: How UpCloud Object Storage behaves for per-app users and buckets
kind: fact
area: data
tags: [upcloud, s3, object-storage, iam, policy, maxcloud]
learned: 2026-11-05
updated: 2026-11-06
where:
  - infra/storage/maxcloud.tf
  - infrastructure/controllers/maxcloud/config.yaml
---

# How UpCloud Object Storage behaves for per-app users and buckets

These were checked against the `maxcloud-apps` instance on 2026-11-05 by
the maxcloud controller's integration test
(`controller/internal/objectstorage/integration_test.go` in the maxcloud
repo):

- **Creating a bucket that already exists** returns `400 Validation error`
  with the invalid parameter `(root): "Bucket already exists".`. It does
  not return 409, so "create if missing" code has to recognise this 400.
- **IAM-style user policies work as on AWS, including `Deny`.** A `Deny`
  on one object (the restore marker `mc-<app>--db/maxcloud.json`) wins
  over an `Allow` on `mc-<app>--*/*`. A user with only object actions
  can't `CreateBucket` or `PutBucketPolicy`; both return 403.
- **Public read** works with a bucket policy of
  `"Principal": "*", "Action": ["s3:GetObject"]`. Anonymous listing stays
  denied. Path-style URLs (`https://<host>/<bucket>/<key>`) and
  virtual-hosted URLs (`https://<bucket>.<host>/<key>`) both work.
- **Presigned PUT** from a plain HTTP client works.
- **Changing a policy** means creating a new default version. UpCloud
  keeps at most 5 versions, so delete the old ones first.
- **Deleted buckets** keep showing up for a while in the bucket list and
  metrics (`upctl object-storage bucket list <uuid> -o json`) with
  `"deleted": true`.
- **A missing key in a private bucket** returns `404 NoSuchKey` to an
  anonymous request, not `403`. That reveals whether a key exists, not
  what's in it.
- **A Host header with `:443`** (`<bucket>.<host>:443`, as Traefik sends
  with `passHostHeader: false`) still finds the bucket.
- **A write that sets an ACL needs `s3:PutObjectAcl`**, even
  `x-amz-acl: private`: without it the PUT is `403 AccessDenied` while the
  same PUT without the header succeeds (2026-11-06, the registry's user
  `zot`). Distribution's S3 driver, which zot uses, always sends it.
- **Quick checks: `curl --aws-sigv4 "aws:amz:europe-1:s3" --user KEY:SECRET`.**
  boto3 1.43 (Python 3.14) got `AccessDenied` for every call, including
  with full-access keys that work from curl and the Go SDK; don't trust it
  here.
- **New access keys and policies** worked within the test's first try or
  two, so they apply within seconds.

The AWS SDK still needs the checksum settings from
[upcloud-s3-checksums](upcloud-s3-checksums.md).

See also: [upcloud-sub-account-names](../ops/upcloud-sub-account-names.md),
[maxcloud-database-recovery](maxcloud-database-recovery.md).
