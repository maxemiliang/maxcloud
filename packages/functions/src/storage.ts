// Buckets an app gets from the registry (`mcloud apps set <app> --buckets
// uploads`). The maxcloud controller names them mc-<app>--<bucket> and puts
// the app's credentials in the Secret maxcloud-s3, which every function
// gets as environment variables: the AWS_* ones any S3 client reads
// (endpoint, region, keys, UpCloud's checksum settings) and MAXCLOUD_APP.

/**
 * The full name of one of the app's buckets: `bucketName("uploads")` is
 * `mc-shop--uploads` in app shop. Use it with any S3 client; the AWS SDK
 * picks up the endpoint and credentials from the environment by itself:
 *
 * ```ts
 * import { S3Client, PutObjectCommand } from "@aws-sdk/client-s3";
 * const s3 = new S3Client({ forcePathStyle: true });
 * await s3.send(new PutObjectCommand({ Bucket: bucketName("uploads"), Key: "a.txt", Body: "hi" }));
 * ```
 */
export function bucketName(name: string, env: Record<string, string | undefined> = process.env): string {
  const app = env.MAXCLOUD_APP;
  if (!app) {
    throw new Error(
      `bucketName(${JSON.stringify(name)}): MAXCLOUD_APP isn't set. The app has no buckets yet ` +
        "(mcloud apps set <app> --buckets ...), or this isn't running on maxcloud.",
    );
  }
  if (!/^[a-z0-9]([a-z0-9]|-[a-z0-9])*$/.test(name)) {
    throw new Error(`bucketName(${JSON.stringify(name)}): lowercase letters, digits and single dashes`);
  }
  return `mc-${app}--${name}`;
}
