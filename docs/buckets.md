# Buckets

An app gets S3 buckets (in the homelab's UpCloud Object Storage, the way a
Worker gets R2) from `buckets:` in `maxcloud.yaml`. The
[controller](../controller/README.md) turns each into a bucket
`mc-shop--uploads` and puts the app's credentials in the Secret
`maxcloud-s3`, which every function gets as environment variables. The
SDKs' `bucketName("uploads")` gives the full name; so does the variable
`MAXCLOUD_BUCKET_UPLOADS` (for frameworks configured by environment; a pod
started before the bucket existed doesn't have it). Removing a bucket from
the list keeps it and its objects.

## Public buckets

Add `:public` to a bucket's name in the list, and anyone can read its
objects (not list them) at a hostname of its own:

```yaml
buckets: [uploads, assets:public]
```

```
https://assets--shop.gronblom.dev/logo.png      # object logo.png in mc-shop--assets
```

- It's for files you'd put on a CDN: images, downloads, built front-end
  assets. Only GET and HEAD go through; writing still takes the app's
  credentials (from a function, or a presigned URL it hands out).
- Cloudflare serves the hostname and caches what it serves, by file
  extension and the object's `Cache-Control`. A changed object can be
  served stale until that runs out, so give changed files new keys
  (`app.3f2a91.js`), or set `Cache-Control` when you upload.
- The same objects are also readable straight from Object Storage, at
  `<AWS_ENDPOINT_URL_S3>/mc-shop--assets/logo.png` (not cached).
- `kubectl get maxcloud -n shop` shows each bucket's URL.
- Removing `:public` makes the bucket private again: its hostname and the
  public read go, its objects stay.
- Public functions are at `<function>--<app>.gronblom.dev` too: don't give
  a public bucket the name of a public function of the same app.
