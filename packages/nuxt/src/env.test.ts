import { test } from "node:test";
import assert from "node:assert/strict";
import { expand, maxcloudMap, nuxtEnv, nuxtEnvName } from "./env.ts";

const env = {
  MAXCLOUD_APP: "paja",
  DATABASE_URL: "postgresql://app:pw@db-rw.paja:5432/app",
  AWS_ACCESS_KEY_ID: "KEY",
  AWS_SECRET_ACCESS_KEY: "SECRET",
  AWS_ENDPOINT_URL_S3: "https://tl8dc.upcloudobjects.com",
  AWS_REGION: "europe-1",
  MAXCLOUD_BUCKET_UPLOADS: "mc-paja--uploads",
};

test("NUXT_* names are spelled as Nitro reads them", () => {
  assert.equal(nuxtEnvName("databaseUrl"), "NUXT_DATABASE_URL");
  assert.equal(nuxtEnvName("storage.s3.accessKeyId"), "NUXT_STORAGE_S3_ACCESS_KEY_ID");
  assert.equal(nuxtEnvName("maxcloud.buckets.user-files"), "NUXT_MAXCLOUD_BUCKETS_USER_FILES");
  assert.equal(nuxtEnvName("public.apiBase"), "NUXT_PUBLIC_API_BASE");
});

test("lankkupaja's settings, without a mapping per variable", () => {
  const out = nuxtEnv({ databaseUrl: "database", "storage.s3": "s3:uploads" }, env);
  assert.deepEqual(out, {
    NUXT_DATABASE_URL: env.DATABASE_URL,
    NUXT_STORAGE_S3_ENDPOINT: env.AWS_ENDPOINT_URL_S3,
    NUXT_STORAGE_S3_REGION: "europe-1",
    NUXT_STORAGE_S3_ACCESS_KEY_ID: "KEY",
    NUXT_STORAGE_S3_SECRET_ACCESS_KEY: "SECRET",
    NUXT_STORAGE_S3_BUCKET: "mc-paja--uploads",
  });
});

test("runtimeConfig.maxcloud", () => {
  const out = nuxtEnv(maxcloudMap(["uploads", "user-files"]), env);
  assert.equal(out.NUXT_MAXCLOUD_APP, "paja");
  assert.equal(out.NUXT_MAXCLOUD_DATABASE_URL, env.DATABASE_URL);
  assert.equal(out.NUXT_MAXCLOUD_S3_SECRET_ACCESS_KEY, "SECRET");
  assert.equal(out.NUXT_MAXCLOUD_BUCKETS_UPLOADS, "mc-paja--uploads");
  // No MAXCLOUD_BUCKET_USER_FILES yet (a pod older than the bucket): the SDKs' rule.
  assert.equal(out.NUXT_MAXCLOUD_BUCKETS_USER_FILES, "mc-paja--user-files");
});

test("what's set already wins, and what's missing stays unset", () => {
  const out = nuxtEnv({ databaseUrl: "database", "storage.s3": "s3:uploads", secret: "env:NOPE" }, {
    NUXT_DATABASE_URL: "postgres://mine", MAXCLOUD_APP: "paja",
  });
  assert.deepEqual(out, {}, "no credentials: no bucket name either, so an app can tell S3 isn't there");
});

test("expand", () => {
  assert.deepEqual(Object.keys(expand({ "a.s3": "s3:x" })).sort(), ["a.s3.accessKeyId", "a.s3.bucket", "a.s3.endpoint", "a.s3.region", "a.s3.secretAccessKey"]);
  assert.throws(() => nuxtEnv({ x: "nope" as never }, env), /unknown source/);
});
