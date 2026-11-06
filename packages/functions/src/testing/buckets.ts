// An in-memory S3 server for tests: enough of the S3 API for the AWS SDK,
// aws4fetch or unstorage's s3 driver to put, get, list and delete objects,
// on 127.0.0.1 with the AWS_* variables a function gets on maxcloud pointing
// at it. Signatures aren't checked.

import { createHash, randomUUID } from "node:crypto";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { bucketName } from "../storage.ts";

export interface TestBucketsOptions {
  /** Buckets to make, by the names the app uses (`uploads` for `bucketName("uploads")`). */
  buckets?: string[];
  /**
   * Set the AWS_* variables a function gets on maxcloud (endpoint, keys,
   * region, checksum settings) to this server's until `close()`, and
   * MAXCLOUD_APP if it isn't set. Default true.
   */
  use?: boolean;
  /** MAXCLOUD_APP when it isn't set. Default "test". */
  app?: string;
}

export interface TestObject {
  body: Buffer;
  /** Content-Type, Cache-Control, x-amz-meta-* and the like, as they were put. */
  headers: Record<string, string>;
  etag: string;
  lastModified: Date;
}

export interface TestBuckets {
  /** The server's URL, http://127.0.0.1:PORT (AWS_ENDPOINT_URL_S3). */
  readonly endpoint: string;
  /** The AWS_* variables for a client of this server. */
  readonly env: Record<string, string>;
  /** Makes a bucket by its full name (`bucketName("uploads")`). */
  createBucket(bucket: string): void;
  /** An object's body, or undefined. */
  get(bucket: string, key: string): Buffer | undefined;
  /** An object with its headers, or undefined. */
  object(bucket: string, key: string): TestObject | undefined;
  /** Puts an object, as a test's starting point. */
  put(bucket: string, key: string, body: string | Uint8Array, contentType?: string): void;
  /** The bucket's keys in order, optionally under a prefix. */
  keys(bucket: string, prefix?: string): string[];
  /** Empties every bucket (they stay). */
  clear(): void;
  /** Stops the server and puts the environment back. */
  close(): Promise<void>;
}

/**
 * Starts an in-memory S3 server and points the environment at it, so the
 * app's S3 client talks to it:
 *
 * ```ts
 * const s3 = await testBuckets({ buckets: ["uploads"] });
 * // ... call code that puts to bucketName("uploads") ...
 * assert.equal(s3.get(bucketName("uploads"), "a.txt")?.toString(), "hi");
 * await s3.close();
 * ```
 *
 * It speaks path-style S3 (`forcePathStyle: true` in the AWS SDK; an IP
 * endpoint makes it path-style by itself) and supports PutObject,
 * GetObject (with Range), HeadObject, DeleteObject, DeleteObjects,
 * CopyObject, ListObjectsV2 and ListObjects, and creating, listing and
 * deleting buckets. Multipart uploads answer 501.
 */
export async function testBuckets(options: TestBucketsOptions = {}): Promise<TestBuckets> {
  const store = new Map<string, Map<string, TestObject>>();
  const server = createServer((req, res) => {
    handle(store, req, res).catch((err) => {
      if (!res.headersSent) sendError(res, req.method, 500, "InternalError", String(err));
      else res.destroy();
    });
  });
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => resolve());
  });
  const endpoint = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  const env: Record<string, string> = {
    AWS_ENDPOINT_URL_S3: endpoint,
    AWS_ACCESS_KEY_ID: "test",
    AWS_SECRET_ACCESS_KEY: "test",
    AWS_REGION: "us-east-1",
    AWS_REQUEST_CHECKSUM_CALCULATION: "when_required",
    AWS_RESPONSE_CHECKSUM_VALIDATION: "when_required",
  };

  // What was there before, to put back on close.
  const saved = new Map<string, string | undefined>();
  const set: Record<string, string> = {};
  if (options.use ?? true) {
    Object.assign(set, env, process.env.MAXCLOUD_APP ? {} : { MAXCLOUD_APP: options.app ?? "test" });
    for (const [k, v] of Object.entries(set)) {
      saved.set(k, process.env[k]);
      process.env[k] = v;
    }
  }
  const app = process.env.MAXCLOUD_APP ?? options.app ?? "test";
  for (const b of options.buckets ?? []) store.set(bucketName(b, { MAXCLOUD_APP: app }), new Map());

  const bucketOf = (bucket: string) => {
    const b = store.get(bucket);
    if (!b) throw new Error(`testBuckets: no bucket ${bucket} (have ${[...store.keys()].join(", ") || "none"})`);
    return b;
  };
  let closed: Promise<void> | undefined;
  return {
    endpoint,
    env,
    createBucket(bucket) {
      if (!store.has(bucket)) store.set(bucket, new Map());
    },
    get: (bucket, key) => bucketOf(bucket).get(key)?.body,
    object: (bucket, key) => bucketOf(bucket).get(key),
    put(bucket, key, body, contentType = "application/octet-stream") {
      bucketOf(bucket).set(key, newObject(Buffer.from(body), { "content-type": contentType }));
    },
    keys: (bucket, prefix = "") => sortedKeys(bucketOf(bucket)).filter((k) => k.startsWith(prefix)),
    clear() {
      for (const b of store.values()) b.clear();
    },
    close() {
      closed ??= new Promise<void>((resolve) => {
        for (const [k, v] of saved) {
          if (process.env[k] !== set[k]) continue; // changed since: not ours to put back
          if (v === undefined) delete process.env[k];
          else process.env[k] = v;
        }
        server.close(() => resolve());
        server.closeAllConnections();
      });
      return closed;
    },
  };
}

// Headers kept with an object and sent back with it.
const kept = ["content-type", "cache-control", "content-disposition", "content-encoding", "content-language", "expires"];

function newObject(body: Buffer, headers: Record<string, string>): TestObject {
  return { body, headers, etag: `"${createHash("md5").update(body).digest("hex")}"`, lastModified: new Date() };
}

function objectHeaders(req: IncomingMessage): Record<string, string> {
  const h: Record<string, string> = {};
  for (const [k, v] of Object.entries(req.headers)) {
    if (typeof v !== "string") continue;
    if (k === "content-encoding") {
      const rest = v.split(",").map((s) => s.trim()).filter((s) => s && s !== "aws-chunked");
      if (rest.length) h[k] = rest.join(", ");
    } else if (kept.includes(k) || k.startsWith("x-amz-meta-")) {
      h[k] = v;
    }
  }
  h["content-type"] ??= "binary/octet-stream";
  return h;
}

// S3 orders keys by their UTF-8 bytes.
const compareKeys = (x: string, y: string) => Buffer.compare(Buffer.from(x), Buffer.from(y));
const sortedKeys = (b: Map<string, TestObject>) => [...b.keys()].sort(compareKeys);

async function readBody(req: IncomingMessage): Promise<Buffer> {
  const chunks: Buffer[] = [];
  for await (const c of req) chunks.push(c as Buffer);
  const body = Buffer.concat(chunks);
  const chunked =
    (req.headers["content-encoding"] ?? "").includes("aws-chunked") ||
    String(req.headers["x-amz-content-sha256"] ?? "").startsWith("STREAMING-");
  return chunked ? decodeAwsChunked(body) : body;
}

// aws-chunked: "<hex size>[;chunk-signature=...]\r\n<data>\r\n" ... "0\r\n<trailers>\r\n".
function decodeAwsChunked(buf: Buffer): Buffer {
  const parts: Buffer[] = [];
  let i = 0;
  while (i < buf.length) {
    const eol = buf.indexOf("\r\n", i);
    if (eol < 0) break;
    const size = parseInt(buf.subarray(i, eol).toString("latin1").split(";")[0]!, 16);
    if (!(size > 0)) break;
    parts.push(buf.subarray(eol + 2, eol + 2 + size));
    i = eol + 2 + size + 2;
  }
  return Buffer.concat(parts);
}

const xmlEscape = (s: string) =>
  s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&apos;" })[c]!);
const xmlUnescape = (s: string) =>
  s.replace(/&(amp|lt|gt|quot|apos|#x?[0-9a-fA-F]+);/g, (m, e: string) => {
    const named: Record<string, string> = { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'" };
    if (named[e]) return named[e];
    const code = e.startsWith("#x") ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10);
    return Number.isFinite(code) ? String.fromCodePoint(code) : m;
  });

function sendXml(res: ServerResponse, status: number, body: string): void {
  const b = Buffer.from(`<?xml version="1.0" encoding="UTF-8"?>\n${body}`);
  res.writeHead(status, { "content-type": "application/xml", "content-length": b.length, "x-amz-request-id": randomUUID() });
  res.end(b);
}

function sendError(res: ServerResponse, method: string | undefined, status: number, code: string, message: string, extra = ""): void {
  if (method === "HEAD") {
    res.writeHead(status, { "x-amz-request-id": randomUUID() });
    res.end();
    return;
  }
  sendXml(res, status, `<Error><Code>${code}</Code><Message>${xmlEscape(message)}</Message>${extra}</Error>`);
}

async function handle(store: Map<string, Map<string, TestObject>>, req: IncomingMessage, res: ServerResponse): Promise<void> {
  const method = req.method ?? "GET";
  const u = new URL(req.url ?? "/", "http://s3");
  const q = u.searchParams;
  // Path-style (/bucket/key), or virtual-hosted (bucket.localhost/key).
  const host = (req.headers.host ?? "").replace(/:\d+$/, "");
  let rawBucket: string, rawKey: string;
  if (host.endsWith(".localhost")) {
    rawBucket = host.slice(0, -".localhost".length);
    rawKey = u.pathname.slice(1);
  } else {
    const path = u.pathname.slice(1);
    const slash = path.indexOf("/");
    rawBucket = slash < 0 ? path : path.slice(0, slash);
    rawKey = slash < 0 ? "" : path.slice(slash + 1);
  }
  const bucket = decodeURIComponent(rawBucket);
  const key = decodeURIComponent(rawKey);
  const noBucket = () =>
    sendError(res, method, 404, "NoSuchBucket", "The specified bucket does not exist", `<BucketName>${xmlEscape(bucket)}</BucketName>`);

  if (!bucket) {
    if (method !== "GET") return sendError(res, method, 405, "MethodNotAllowed", `${method} /`);
    const list = [...store.keys()].sort().map((b) => `<Bucket><Name>${xmlEscape(b)}</Name><CreationDate>${new Date().toISOString()}</CreationDate></Bucket>`);
    return sendXml(res, 200, `<ListAllMyBucketsResult><Owner><ID>test</ID></Owner><Buckets>${list.join("")}</Buckets></ListAllMyBucketsResult>`);
  }
  if (q.has("uploads") || q.has("uploadId")) {
    await readBody(req);
    return sendError(res, method, 501, "NotImplemented", "testBuckets doesn't do multipart uploads");
  }
  const b = store.get(bucket);

  if (!key) {
    switch (method) {
      case "PUT":
        await readBody(req);
        if (!b) store.set(bucket, new Map());
        res.writeHead(200, { location: "/" + bucket });
        return void res.end();
      case "HEAD":
        if (!b) return noBucket();
        res.writeHead(200);
        return void res.end();
      case "DELETE":
        if (!b) return noBucket();
        if (b.size > 0) return sendError(res, method, 409, "BucketNotEmpty", "The bucket you tried to delete is not empty");
        store.delete(bucket);
        res.writeHead(204);
        return void res.end();
      case "POST":
        if (!q.has("delete")) return sendError(res, method, 405, "MethodNotAllowed", `POST /${bucket}`);
        if (!b) return noBucket();
        return deleteObjects(b, (await readBody(req)).toString("utf8"), res);
      case "GET":
        if (!b) return noBucket();
        if (q.has("location")) return sendXml(res, 200, `<LocationConstraint/>`);
        return list(bucket, b, q, res);
      default:
        return sendError(res, method, 405, "MethodNotAllowed", `${method} /${bucket}`);
    }
  }

  if (!b) {
    await readBody(req);
    return noBucket();
  }
  switch (method) {
    case "PUT": {
      const source = req.headers["x-amz-copy-source"];
      if (typeof source === "string") {
        await readBody(req);
        return copyObject(store, b, key, source, req, res);
      }
      const obj = newObject(await readBody(req), objectHeaders(req));
      b.set(key, obj);
      res.writeHead(200, { etag: obj.etag });
      return void res.end();
    }
    case "GET":
    case "HEAD": {
      const obj = b.get(key);
      if (!obj) return sendError(res, method, 404, "NoSuchKey", "The specified key does not exist.", `<Key>${xmlEscape(key)}</Key>`);
      const headers: Record<string, string | number> = {
        ...obj.headers,
        etag: obj.etag,
        "last-modified": obj.lastModified.toUTCString(),
        "accept-ranges": "bytes",
      };
      let body = obj.body;
      let status = 200;
      const range = /^bytes=(\d*)-(\d*)$/.exec(String(req.headers.range ?? ""));
      if (range && (range[1] || range[2])) {
        const size = obj.body.length;
        let start: number, end: number;
        if (range[1]) {
          start = Number(range[1]);
          end = range[2] ? Math.min(Number(range[2]), size - 1) : size - 1;
        } else {
          start = Math.max(0, size - Number(range[2]));
          end = size - 1;
        }
        if (start >= size || start > end) {
          return sendError(res, method, 416, "InvalidRange", "The requested range is not satisfiable");
        }
        body = obj.body.subarray(start, end + 1);
        status = 206;
        headers["content-range"] = `bytes ${start}-${end}/${size}`;
      }
      headers["content-length"] = body.length;
      res.writeHead(status, headers);
      return void res.end(method === "HEAD" ? undefined : body);
    }
    case "DELETE":
      b.delete(key);
      res.writeHead(204);
      return void res.end();
    default:
      await readBody(req);
      return sendError(res, method, 405, "MethodNotAllowed", `${method} /${bucket}/${key}`);
  }
}

function list(bucket: string, b: Map<string, TestObject>, q: URLSearchParams, res: ServerResponse): void {
  const v2 = q.get("list-type") === "2";
  const prefix = q.get("prefix") ?? "";
  const delimiter = q.get("delimiter") ?? "";
  const maxKeys = Math.min(Math.max(Number(q.get("max-keys") ?? 1000) || 0, 0), 1000);
  const token = q.get("continuation-token");
  const after = v2 ? (token ? Buffer.from(token, "base64url").toString("utf8") : (q.get("start-after") ?? "")) : (q.get("marker") ?? "");
  const enc = q.get("encoding-type") === "url" ? (s: string) => encodeURIComponent(s).replace(/%2F/g, "/") : (s: string) => s;

  const contents: string[] = [];
  const prefixes: string[] = [];
  let last = "";
  let truncated = false;
  for (const key of sortedKeys(b)) {
    if (!key.startsWith(prefix)) continue;
    if (after && (compareKeys(key, after) <= 0 || (delimiter && after.endsWith(delimiter) && key.startsWith(after)))) continue;
    let common: string | undefined;
    if (delimiter) {
      const i = key.indexOf(delimiter, prefix.length);
      if (i >= 0) common = key.slice(0, i + delimiter.length);
    }
    if (common !== undefined && common === prefixes[prefixes.length - 1]) continue;
    if (contents.length + prefixes.length >= maxKeys) {
      truncated = true;
      break;
    }
    if (common !== undefined) {
      prefixes.push(common);
      last = common;
    } else {
      const o = b.get(key)!;
      contents.push(
        `<Contents><Key>${xmlEscape(enc(key))}</Key><LastModified>${o.lastModified.toISOString()}</LastModified>` +
          `<ETag>${xmlEscape(o.etag)}</ETag><Size>${o.body.length}</Size><StorageClass>STANDARD</StorageClass></Contents>`,
      );
      last = key;
    }
  }
  const prefixXml = prefixes.map((p) => `<CommonPrefixes><Prefix>${xmlEscape(enc(p))}</Prefix></CommonPrefixes>`).join("");
  const common = [
    `<Name>${xmlEscape(bucket)}</Name>`,
    `<Prefix>${xmlEscape(enc(prefix))}</Prefix>`,
    delimiter ? `<Delimiter>${xmlEscape(enc(delimiter))}</Delimiter>` : "",
    `<MaxKeys>${maxKeys}</MaxKeys>`,
    q.get("encoding-type") === "url" ? `<EncodingType>url</EncodingType>` : "",
    `<IsTruncated>${truncated}</IsTruncated>`,
  ].join("");
  if (v2) {
    const extra = [
      `<KeyCount>${contents.length + prefixes.length}</KeyCount>`,
      token ? `<ContinuationToken>${xmlEscape(token)}</ContinuationToken>` : "",
      q.has("start-after") ? `<StartAfter>${xmlEscape(enc(q.get("start-after")!))}</StartAfter>` : "",
      truncated ? `<NextContinuationToken>${Buffer.from(last).toString("base64url")}</NextContinuationToken>` : "",
    ].join("");
    return sendXml(res, 200, `<ListBucketResult>${common}${extra}${contents.join("")}${prefixXml}</ListBucketResult>`);
  }
  const marker = `<Marker>${xmlEscape(enc(after))}</Marker>` + (truncated ? `<NextMarker>${xmlEscape(enc(last))}</NextMarker>` : "");
  sendXml(res, 200, `<ListBucketResult>${common}${marker}${contents.join("")}${prefixXml}</ListBucketResult>`);
}

function deleteObjects(b: Map<string, TestObject>, body: string, res: ServerResponse): void {
  const quiet = /<Quiet>\s*true\s*<\/Quiet>/i.test(body);
  const deleted: string[] = [];
  for (const m of body.matchAll(/<Key>([\s\S]*?)<\/Key>/g)) {
    const key = xmlUnescape(m[1]!);
    b.delete(key);
    if (!quiet) deleted.push(`<Deleted><Key>${xmlEscape(key)}</Key></Deleted>`);
  }
  sendXml(res, 200, `<DeleteResult>${deleted.join("")}</DeleteResult>`);
}

function copyObject(
  store: Map<string, Map<string, TestObject>>,
  b: Map<string, TestObject>,
  key: string,
  source: string,
  req: IncomingMessage,
  res: ServerResponse,
): void {
  const path = decodeURIComponent(source.split("?")[0]!).replace(/^\//, "");
  const slash = path.indexOf("/");
  const from = store.get(path.slice(0, slash))?.get(path.slice(slash + 1));
  if (slash < 0 || !from) return sendError(res, "PUT", 404, "NoSuchKey", "The specified key does not exist.", `<Key>${xmlEscape(path)}</Key>`);
  const headers = req.headers["x-amz-metadata-directive"] === "REPLACE" ? objectHeaders(req) : { ...from.headers };
  const obj = newObject(Buffer.from(from.body), headers);
  b.set(key, obj);
  sendXml(res, 200, `<CopyObjectResult><LastModified>${obj.lastModified.toISOString()}</LastModified><ETag>${xmlEscape(obj.etag)}</ETag></CopyObjectResult>`);
}
