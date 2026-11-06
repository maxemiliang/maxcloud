import { test } from "node:test";
import assert from "node:assert/strict";
import { request } from "node:http";
import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";
import { onQueue, onRequest, onSchedule } from "./define.ts";
import { hostOnly, startDev } from "./dev.ts";
import { enqueue } from "./queue.ts";
import { findFunctions } from "./runtime.ts";

const quiet = () => {};

/** A request with any Host header (fetch won't set one). */
function get(port: number, host: string, path = "/", method = "GET", body?: string): Promise<{ status: number; body: string; type: string }> {
  return new Promise((resolve, reject) => {
    const req = request({ host: "127.0.0.1", port, path, method, headers: { host } }, (res) => {
      let data = "";
      res.setEncoding("utf8");
      res.on("data", (d) => (data += d));
      res.on("end", () => resolve({ status: res.statusCode ?? 0, body: data, type: String(res.headers["content-type"] ?? "") }));
    });
    req.on("error", reject);
    req.end(body);
  });
}

function app() {
  return findFunctions({
    apiServer: onRequest(async (req) => {
      const body = req.method === "POST" ? await req.text() : "";
      return new Response(`api ${req.method} ${new URL(req.url).pathname}${new URL(req.url).search} ${req.headers.get("x-in") ?? ""} ${body}`);
    }),
    web: onRequest(() => new Response("web")),
    nightly: onSchedule("0 3 * * *", async () => {
      if (process.env.FAIL_NIGHTLY) throw new Error("nightly broke");
    }),
    resize: onQueue("images", async () => {}),
  });
}

async function start(t: import("node:test").TestContext, found = app()) {
  const stop = new AbortController();
  const printed: string[] = [];
  const dev = await startDev(found, { port: 0, signal: stop.signal, log: quiet, print: (m) => printed.push(m) });
  t.after(async () => {
    stop.abort();
    await dev.stopped;
  });
  return { ...dev, printed, stop };
}

test("hostOnly drops the port", () => {
  assert.equal(hostOnly("Api.Localhost:8080"), "api.localhost");
  assert.equal(hostOnly("localhost"), "localhost");
  assert.equal(hostOnly("[::1]:8080"), "[::1]");
  assert.equal(hostOnly(undefined), "");
});

test("<name>.localhost reaches that HTTP function, request unchanged", async (t) => {
  const { port } = await start(t);
  const res = await new Promise<string>((resolve, reject) => {
    const req = request({ host: "127.0.0.1", port, path: "/a/b?c=1", method: "POST", headers: { host: `api-server.localhost:${port}`, "x-in": "yes" } }, (r) => {
      let d = "";
      r.on("data", (c) => (d += c));
      r.on("end", () => resolve(d));
    });
    req.on("error", reject);
    req.end("body");
  });
  assert.equal(res, "api POST /a/b?c=1 yes body");
  assert.equal((await get(port, "web.localhost")).body, "web");
  const missing = await get(port, "nope.localhost");
  assert.equal(missing.status, 404);
  assert.match(missing.body, /no HTTP function nope/);
  assert.equal((await get(port, "nightly.localhost")).status, 404, "only HTTP functions have a host");
});

test("GET / lists the functions", async (t) => {
  const { port } = await start(t);
  for (const host of [`localhost:${port}`, `127.0.0.1:${port}`]) {
    const res = await get(port, host);
    assert.equal(res.status, 200);
    assert.match(res.type, /json/);
    assert.deepEqual(JSON.parse(res.body), [
      { kind: "http", name: "api-server", url: `http://api-server.localhost:${port}/` },
      { kind: "schedule", name: "nightly" },
      { kind: "queue", name: "resize" },
      { kind: "http", name: "web", url: `http://web.localhost:${port}/` },
    ]);
  }
});

test("POST /_run/<name> runs a schedule once", async (t) => {
  const { port } = await start(t);
  const ok = await get(port, "localhost", "/_run/nightly", "POST");
  assert.equal(ok.status, 200);
  assert.equal(ok.body, "ok\n");

  process.env.FAIL_NIGHTLY = "1";
  t.after(() => delete process.env.FAIL_NIGHTLY);
  const failed = await get(port, "localhost", "/_run/nightly", "POST");
  assert.equal(failed.status, 500);
  assert.equal(failed.body, "nightly broke\n");

  for (const name of ["nope", "web", "resize"]) {
    const res = await get(port, "localhost", `/_run/${name}`, "POST");
    assert.equal(res.status, 404, name);
    assert.match(res.body, new RegExp(`no scheduled function ${name}`));
  }
});

test("anything else on localhost is a 404 that points at /", async (t) => {
  const { port } = await start(t);
  for (const [method, path] of [["GET", "/other"], ["POST", "/"], ["GET", "/_run/nightly"]] as const) {
    const res = await get(port, "localhost", path, method);
    assert.equal(res.status, 404, `${method} ${path}`);
    assert.match(res.body, new RegExp(`GET http://localhost:${port}/ lists the functions`));
  }
});

test("without DATABASE_URL, queue workers don't start and the table says so", async (t) => {
  const saved = process.env.DATABASE_URL;
  delete process.env.DATABASE_URL;
  t.after(() => {
    if (saved !== undefined) process.env.DATABASE_URL = saved;
  });
  const { port, printed } = await start(t);
  const table = printed.join("\n");
  assert.match(table, new RegExp(`http +api-server +http://api-server\\.localhost:${port}/`));
  assert.match(table, new RegExp(`schedule +nightly +POST http://localhost:${port}/_run/nightly`));
  assert.match(table, /queue +resize +not running \(no DATABASE_URL\)/);
  assert.match(table, /queue workers don't run without DATABASE_URL \(mcloud dev provides one\)/);
  // It would throw (and be logged) if a worker had tried to connect; the server still answers.
  assert.equal((await get(port, "web.localhost")).body, "web");
});

test("stopping lets requests in flight finish", async () => {
  let release!: () => void;
  const gate = new Promise<void>((r) => (release = r));
  const found = findFunctions({ slow: onRequest(async () => { await gate; return new Response("finished"); }) });
  const stop = new AbortController();
  const dev = await startDev(found, { port: 0, signal: stop.signal, log: quiet, print: quiet });
  const pending = get(dev.port, "slow.localhost");
  await new Promise((r) => setTimeout(r, 50));
  stop.abort();
  setTimeout(release, 100);
  assert.equal((await pending).body, "finished");
  await dev.stopped;
  await assert.rejects(get(dev.port, "slow.localhost"));
});

const url = process.env.TEST_DATABASE_URL;

test("with DATABASE_URL, queue workers run in-process", { skip: url ? false : "set TEST_DATABASE_URL to run the queue tests" }, async (t) => {
  process.env.DATABASE_URL = url;
  t.after(async () => {
    const { db } = await import("./db.ts");
    await db().end();
  });
  const queue = "d" + randomUUID().slice(0, 8);
  let got: unknown;
  const found = findFunctions({ work: onQueue<{ n: number }>(queue, async (job) => { got = job.payload; }) });
  const { printed } = await start(t, found);
  assert.match(printed.join("\n"), /queue +work +worker running/);
  await enqueue(queue, { n: 7 });
  const deadline = Date.now() + 5000;
  while (got === undefined && Date.now() < deadline) await new Promise((r) => setTimeout(r, 25));
  assert.deepEqual(got, { n: 7 });
});

test("maxcloud-functions dev serves the app and exits 0 on SIGTERM", async () => {
  const cli = fileURLToPath(new URL("./cli.ts", import.meta.url));
  const entry = fileURLToPath(new URL("./testdata/app.ts", import.meta.url));
  const env = { ...process.env };
  delete env.DATABASE_URL;
  const child = spawn(process.execPath, [cli, "dev", "--entry", entry, "--port", "0"], { env });
  let out = "";
  child.stdout.on("data", (d) => (out += d));
  child.stderr.on("data", (d) => (out += d));
  const exited = new Promise<number | null>((r) => child.on("exit", (code) => r(code)));
  let port = 0;
  for (const deadline = Date.now() + 5000; !port && Date.now() < deadline; ) {
    port = Number(/http:\/\/localhost:(\d+)\/ lists/.exec(out)?.[1] ?? 0);
    await new Promise((r) => setTimeout(r, 20));
  }
  assert.ok(port, out);
  assert.match(out, /resize +not running/);
  const slow = get(port, "hello.localhost", "/?wait=300");
  await new Promise((r) => setTimeout(r, 100));
  child.kill("SIGTERM");
  assert.equal((await slow).body, "hello\n");
  assert.equal(await exited, 0, out);
});
