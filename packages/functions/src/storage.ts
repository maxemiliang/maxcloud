// Buckets an app asks for in its maxcloud.yaml (`buckets: [uploads]`). The
// maxcloud controller names them mc-<app>--<bucket> and puts the app's
// credentials in the Secret maxcloud-s3, which every function gets as the
// AWS_* environment variables any S3 client reads (endpoint, region, keys,
// UpCloud's checksum settings). MAXCLOUD_APP, the app's name, is set on
// every function.

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
      `bucketName(${JSON.stringify(name)}): MAXCLOUD_APP isn't set, so this isn't running on maxcloud ` +
        "(mcloud dev sets it locally)",
    );
  }
  if (!/^[a-z0-9]([a-z0-9]|-[a-z0-9])*$/.test(name)) {
    throw new Error(`bucketName(${JSON.stringify(name)}): lowercase letters, digits and single dashes`);
  }
  return `mc-${app}--${name}`;
}
