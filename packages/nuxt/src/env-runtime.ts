// The part of the env mapping that runs in the server, first thing. No
// imports: whatever it imports is evaluated before it, and a module Nitro
// shares (scule, h3) would pull in Nitro's chunk, which creates the app and
// reads the runtimeConfig before the NUXT_* variables are set.

type Env = Record<string, string | undefined>;

const S3: Record<string, string> = {
  endpoint: "AWS_ENDPOINT_URL_S3",
  region: "AWS_REGION",
  accessKeyId: "AWS_ACCESS_KEY_ID",
  secretAccessKey: "AWS_SECRET_ACCESS_KEY",
};

/** The variable holding a bucket's full name (the controller's BucketEnv). */
export function bucketEnv(bucket: string): string {
  return "MAXCLOUD_BUCKET_" + bucket.toUpperCase().replaceAll("-", "_");
}

/** A bucket's full name: from MAXCLOUD_BUCKET_<NAME>, else mc-<app>--<name>; "" without credentials. */
function bucket(name: string, env: Env): string {
  if (env[bucketEnv(name)]) return env[bucketEnv(name)]!;
  if (!env.AWS_ACCESS_KEY_ID || !env.MAXCLOUD_APP) return "";
  return `mc-${env.MAXCLOUD_APP}--${name}`;
}

/** A source's value (see Source in env.ts). */
export function resolveSource(source: string, env: Env): string | undefined {
  if (source === "app") return env.MAXCLOUD_APP;
  if (source === "database") return env.DATABASE_URL;
  if (source.startsWith("s3.") && S3[source.slice(3)]) return env[S3[source.slice(3)]!];
  if (source.startsWith("bucket:")) return bucket(source.slice(7), env) || undefined;
  if (source.startsWith("env:")) return env[source.slice(4)];
  throw new Error(`@max-cloud/nuxt: unknown source ${JSON.stringify(source)}`);
}

/** The S3 fields an "s3:<bucket>" entry expands to, and their sources. */
export const S3_FIELDS = Object.keys(S3);

/**
 * The NUXT_* variables to set: for each name (NUXT_DATABASE_URL) its
 * source's value, unless the variable is set already or there's no value.
 */
export function nuxtEnvFrom(byName: Record<string, string>, env: Env = process.env): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [name, source] of Object.entries(byName)) {
    if (env[name] !== undefined && env[name] !== "") continue;
    const v = resolveSource(source, env);
    if (v !== undefined && v !== "") out[name] = v;
  }
  return out;
}

/** Sets them in process.env. */
export function applyMaxcloudEnv(byName: Record<string, string>): void {
  Object.assign(process.env, nuxtEnvFrom(byName));
}
