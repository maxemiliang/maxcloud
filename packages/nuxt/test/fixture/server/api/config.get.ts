// What reached runtimeConfig (never the secrets themselves).
export default defineEventHandler(() => {
  const c = useRuntimeConfig();
  return {
    databaseUrl: c.databaseUrl,
    bucket: c.storage.s3.bucket,
    endpoint: c.storage.s3.endpoint,
    hasKey: Boolean(c.storage.s3.accessKeyId && c.storage.s3.secretAccessKey),
    maxcloud: { app: c.maxcloud.app, buckets: c.maxcloud.buckets, hasDatabase: Boolean(c.maxcloud.databaseUrl) },
  };
});
