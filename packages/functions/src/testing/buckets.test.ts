import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { bucketName } from "../storage.ts";
import { testBuckets, type TestBuckets } from "./buckets.ts";

let s3: TestBuckets;
let bucket: string;
let base: string;
const savedApp = process.env.MAXCLOUD_APP;
const savedEndpoint = process.env.AWS_ENDPOINT_URL_S3;

before(async () => {
  delete process.env.MAXCLOUD_APP;
  s3 = await testBuckets({ buckets: ["uploads"], app: "shop" });
  bucket = bucketName("uploads");
  base = `${process.env.AWS_ENDPOINT_URL_S3}/${bucket}`;
});
after(async () => {
  await s3.close();
  assert.equal(process.env.MAXCLOUD_APP, savedApp);
  assert.equal(process.env.AWS_ENDPOINT_URL_S3, savedEndpoint);
});

const listKeys = async (query: string) => {
  const xml = await (await fetch(`${base}?${query}`)).text();
  return {
    keys: [...xml.matchAll(/<Key>(.*?)<\/Key>/g)].map((m) => m[1]!),
    prefixes: [...xml.matchAll(/<CommonPrefixes><Prefix>(.*?)<\/Prefix>/g)].map((m) => m[1]!),
    token: /<NextContinuationToken>(.*?)</.exec(xml)?.[1],
    truncated: /<IsTruncated>true</.test(xml),
    xml,
  };
};

test("sets the environment a function gets on maxcloud", () => {
  assert.equal(process.env.MAXCLOUD_APP, "shop");
  assert.equal(bucket, "mc-shop--uploads");
  assert.equal(process.env.AWS_ENDPOINT_URL_S3, s3.endpoint);
  assert.match(s3.endpoint, /^http:\/\/127\.0\.0\.1:\d+$/);
  for (const k of ["AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION", "AWS_REQUEST_CHECKSUM_CALCULATION"]) {
    assert.ok(process.env[k], k);
  }
});

test("put, get, head and delete an object", async () => {
  const put = await fetch(`${base}/dir/a%20b.txt`, {
    method: "PUT",
    body: "hello",
    headers: { "content-type": "text/plain", "x-amz-meta-owner": "ann" },
  });
  assert.equal(put.status, 200);
  assert.equal(put.headers.get("etag"), '"5d41402abc4b2a76b9719d911017c592"');
  assert.equal(s3.get(bucket, "dir/a b.txt")?.toString(), "hello");

  const get = await fetch(`${base}/dir/a%20b.txt`);
  assert.equal(get.status, 200);
  assert.equal(await get.text(), "hello");
  assert.equal(get.headers.get("content-type"), "text/plain");
  assert.equal(get.headers.get("x-amz-meta-owner"), "ann");
  assert.ok(get.headers.get("last-modified"));

  const range = await fetch(`${base}/dir/a%20b.txt`, { headers: { range: "bytes=1-3" } });
  assert.equal(range.status, 206);
  assert.equal(await range.text(), "ell");
  assert.equal(range.headers.get("content-range"), "bytes 1-3/5");

  const head = await fetch(`${base}/dir/a%20b.txt`, { method: "HEAD" });
  assert.equal(head.status, 200);
  assert.equal(head.headers.get("content-length"), "5");

  assert.equal((await fetch(`${base}/dir/a%20b.txt`, { method: "DELETE" })).status, 204);
  assert.equal(s3.get(bucket, "dir/a b.txt"), undefined);
  const gone = await fetch(`${base}/dir/a%20b.txt`);
  assert.equal(gone.status, 404);
  assert.match(await gone.text(), /<Code>NoSuchKey<\/Code>/);
  assert.equal((await fetch(`${base}/dir/a%20b.txt`, { method: "HEAD" })).status, 404);
  assert.equal((await fetch(`${base}/x`, { method: "DELETE" })).status, 204, "deleting a missing key is fine");
});

test("an unknown bucket is NoSuchBucket", async () => {
  const r = await fetch(`${s3.endpoint}/nope/k`);
  assert.equal(r.status, 404);
  assert.match(await r.text(), /<Code>NoSuchBucket<\/Code>/);
  assert.throws(() => s3.get("nope", "k"), /no bucket nope \(have mc-shop--uploads\)/);
});

test("ListObjectsV2 with prefix, delimiter and pages", async () => {
  s3.clear();
  for (const k of ["a/1", "a/2", "b/1", "c", "d&e", "z/deep/x"]) s3.put(bucket, k, k);

  const all = await listKeys("list-type=2");
  assert.deepEqual(all.keys, ["a/1", "a/2", "b/1", "c", "d&amp;e", "z/deep/x"]);
  assert.match(all.xml, /<KeyCount>6<\/KeyCount>/);
  assert.match(all.xml, /<Size>3<\/Size>/);

  assert.deepEqual((await listKeys("list-type=2&prefix=a/")).keys, ["a/1", "a/2"]);

  const tree = await listKeys("list-type=2&delimiter=/");
  assert.deepEqual(tree.keys, ["c", "d&amp;e"]);
  assert.deepEqual(tree.prefixes, ["a/", "b/", "z/"]);

  // Pages of 2, common prefixes counting as one entry each.
  const seen: string[] = [];
  let token: string | undefined;
  let pages = 0;
  do {
    const page = await listKeys(`list-type=2&delimiter=/&max-keys=2${token ? "&continuation-token=" + token : ""}`);
    seen.push(...page.prefixes.map((p) => p + "*"), ...page.keys.map(String));
    token = page.token;
    assert.equal(page.truncated, token !== undefined);
    pages++;
  } while (token && pages < 10);
  assert.equal(pages, 3);
  assert.deepEqual(seen.sort(), ["a/*", "b/*", "c", "d&amp;e", "z/*"]);

  assert.deepEqual((await listKeys("list-type=2&start-after=b/1")).keys, ["c", "d&amp;e", "z/deep/x"]);
  // ListObjects (v1) with a marker.
  assert.deepEqual((await listKeys("marker=c&max-keys=1")).keys, ["d&amp;e"]);
});

test("DeleteObjects and CopyObject", async () => {
  s3.clear();
  s3.put(bucket, "keep", "k");
  s3.put(bucket, "x&y", "1");
  s3.put(bucket, "two", "2", "text/plain");

  const copy = await fetch(`${base}/copied`, { method: "PUT", headers: { "x-amz-copy-source": `/${bucket}/two` } });
  assert.equal(copy.status, 200);
  assert.match(await copy.text(), /<CopyObjectResult>.*<ETag>/s);
  assert.equal(s3.object(bucket, "copied")?.headers["content-type"], "text/plain");

  const del = await fetch(`${base}?delete`, {
    method: "POST",
    body: `<Delete><Object><Key>x&amp;y</Key></Object><Object><Key>two</Key></Object><Object><Key>missing</Key></Object></Delete>`,
  });
  assert.equal(del.status, 200);
  assert.equal((await del.text()).match(/<Deleted>/g)?.length, 3);
  assert.deepEqual(s3.keys(bucket), ["copied", "keep"]);
});

test("aws-chunked uploads are decoded", async () => {
  const body = "5;chunk-signature=abc\r\nhello\r\n6;chunk-signature=def\r\n world\r\n0\r\nx-amz-checksum-crc32:AAAA\r\n\r\n";
  const r = await fetch(`${base}/chunked`, {
    method: "PUT",
    body,
    headers: { "content-encoding": "aws-chunked", "x-amz-content-sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER" },
  });
  assert.equal(r.status, 200);
  assert.equal(s3.get(bucket, "chunked")?.toString(), "hello world");
  assert.equal(s3.object(bucket, "chunked")?.headers["content-encoding"], undefined);
});

test("buckets can be made, listed and removed", async () => {
  assert.equal((await fetch(`${s3.endpoint}/mc-shop--new`, { method: "PUT" })).status, 200);
  assert.equal((await fetch(`${s3.endpoint}/mc-shop--new`, { method: "HEAD" })).status, 200);
  assert.match(await (await fetch(`${s3.endpoint}/`)).text(), /<Name>mc-shop--new<\/Name>.*<Name>mc-shop--uploads<\/Name>/s);
  assert.equal((await fetch(`${s3.endpoint}/mc-shop--new`, { method: "DELETE" })).status, 204);
  assert.equal((await fetch(`${s3.endpoint}/mc-shop--new`, { method: "HEAD" })).status, 404);
  assert.equal((await fetch(`${s3.endpoint}/${bucket}`, { method: "DELETE" })).status, 409, "not empty");
});

test("multipart uploads say they aren't supported", async () => {
  const r = await fetch(`${base}/big?uploads`, { method: "POST" });
  assert.equal(r.status, 501);
});
