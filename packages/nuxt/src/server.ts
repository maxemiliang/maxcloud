// Server helpers for a Nuxt (Nitro) app on maxcloud:
//
//   import { s3Driver, drizzleDb } from "@max-cloud/nuxt/server";
//   useStorage().mount("files", await s3Driver("uploads"));
//   const db = await drizzleDb(schema);

import { bucketName, db } from "@max-cloud/functions";

export { bucketName, db } from "@max-cloud/functions";

/** An unstorage S3 driver for one of the app's buckets, with its credentials from the environment. */
export async function s3Driver(bucket: string, opts: { region?: string } = {}) {
  const { default: s3 } = await import("unstorage/drivers/s3");
  const need = ["AWS_ENDPOINT_URL_S3", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"].filter((k) => !process.env[k]);
  if (need.length) {
    throw new Error(`@max-cloud/nuxt: bucket ${bucket} needs ${need.join(", ")} (the Secret maxcloud-s3; mcloud dev --buckets locally)`);
  }
  return s3({
    endpoint: process.env.AWS_ENDPOINT_URL_S3!,
    region: opts.region ?? process.env.AWS_REGION ?? "auto",
    bucket: process.env["MAXCLOUD_BUCKET_" + bucket.toUpperCase().replaceAll("-", "_")] || bucketName(bucket),
    accessKeyId: process.env.AWS_ACCESS_KEY_ID!,
    secretAccessKey: process.env.AWS_SECRET_ACCESS_KEY!,
  });
}

/** Drizzle on the app's database, sharing @max-cloud/functions' pool (db()) with enqueue and kv. */
export async function drizzleDb<TSchema extends Record<string, unknown> = Record<string, never>>(schema?: TSchema) {
  const { drizzle } = await import("drizzle-orm/node-postgres");
  return drizzle({ client: db(), schema });
}
