---
title: UpCloud Object Storage rejects the AWS SDK's default checksums
kind: gotcha
area: data
tags: [upcloud, s3, object-storage, cnpg, loki, aws-sdk]
learned: 2026-10-29
updated: 2026-11-05
where:
  - infrastructure/controllers/monitoring/loki.yaml
---

# UpCloud Object Storage rejects the AWS SDK's default checksums

**Symptom:** uploads to UpCloud Object Storage fail from tools built on
current AWS SDKs (CNPG's barman-cloud plugin, Loki).

**Cause:** newer AWS SDKs send integrity checksums on every request by
default, which UpCloud's S3 API doesn't accept.

**Fix:** set both in the client's environment:

```yaml
- name: AWS_REQUEST_CHECKSUM_CALCULATION
  value: when_required
- name: AWS_RESPONSE_CHECKSUM_VALIDATION
  value: when_required
```

Needed by every new S3 client of UpCloud Object Storage.

See also: [upcloud-object-storage-behaviour](upcloud-object-storage-behaviour.md).
