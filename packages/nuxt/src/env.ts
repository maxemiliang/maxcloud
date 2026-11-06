// What maxcloud gives an app (DATABASE_URL, the AWS_* variables of the
// Secret maxcloud-s3, MAXCLOUD_BUCKET_<NAME>, MAXCLOUD_APP) as the NUXT_*
// variables Nuxt reads into runtimeConfig. It runs before Nitro reads its
// runtimeConfig (which it freezes): first thing in the server entry, and
// in the Nuxt process for `nuxt dev`.
//
// It only fills NUXT_* variables that aren't set: a value you set wins.

import { snakeCase } from "scule";
import { nuxtEnvFrom, S3_FIELDS } from "./env-runtime.ts";

/**
 * Where a runtimeConfig value comes from on maxcloud:
 *
 * - `"app"`: the app's name (MAXCLOUD_APP)
 * - `"database"`: the database's URL (DATABASE_URL)
 * - `"s3.endpoint"`, `"s3.region"`, `"s3.accessKeyId"`, `"s3.secretAccessKey"`:
 *   the app's Object Storage credentials (AWS_*)
 * - `"bucket:<name>"`: a bucket's full name, mc-<app>--<name>
 * - `"s3:<bucket>"`: on an object key, all of the above for one bucket:
 *   endpoint, region, accessKeyId, secretAccessKey and bucket
 * - `"env:<NAME>"`: any other environment variable
 */
export type Source = "app" | "database" | "s3.endpoint" | "s3.region" | "s3.accessKeyId" | "s3.secretAccessKey" | `bucket:${string}` | `s3:${string}` | `env:${string}`;

/** runtimeConfig key (dotted path, e.g. "storage.s3.bucket") to its source. */
export type RuntimeConfigMap = Record<string, Source>;

type Env = Record<string, string | undefined>;

export { bucketEnv } from "./env-runtime.ts";

/** The NUXT_* variable Nuxt reads for a runtimeConfig key, as Nitro's applyEnv spells it. */
export function nuxtEnvName(path: string): string {
  return "NUXT_" + snakeCase(path.replaceAll(".", "_")).toUpperCase();
}

/** Expands "s3:<bucket>" entries into one per field. */
export function expand(map: RuntimeConfigMap): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [key, source] of Object.entries(map)) {
    if (source.startsWith("s3:")) {
      for (const field of S3_FIELDS) out[`${key}.${field}`] = `s3.${field}`;
      out[`${key}.bucket`] = `bucket:${source.slice(3)}`;
    } else {
      out[key] = source;
    }
  }
  return out;
}

/** The runtimeConfig.maxcloud the module declares, and where each value comes from. */
export function maxcloudMap(buckets: string[]): RuntimeConfigMap {
  const map: RuntimeConfigMap = {
    "maxcloud.app": "app",
    "maxcloud.databaseUrl": "database",
    "maxcloud.s3.endpoint": "s3.endpoint",
    "maxcloud.s3.region": "s3.region",
    "maxcloud.s3.accessKeyId": "s3.accessKeyId",
    "maxcloud.s3.secretAccessKey": "s3.secretAccessKey",
  };
  for (const b of buckets) map[`maxcloud.buckets.${b}`] = `bucket:${b}`;
  return map;
}

/** The map keyed by NUXT_* variable name, as the server-side part takes it. */
export function byEnvName(map: RuntimeConfigMap): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [key, source] of Object.entries(expand(map))) out[nuxtEnvName(key)] = source;
  return out;
}

/**
 * The NUXT_* variables to set for `map`, given the environment: only those
 * with a value, and not already set.
 */
export function nuxtEnv(map: RuntimeConfigMap, env: Env = process.env): Record<string, string> {
  return nuxtEnvFrom(byEnvName(map), env);
}

/** Sets the variables nuxtEnv computes in process.env (nuxt dev). */
export function applyEnv(map: RuntimeConfigMap): void {
  Object.assign(process.env, nuxtEnv(map));
}
