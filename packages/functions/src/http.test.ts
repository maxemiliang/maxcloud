import { test } from "node:test";
import assert from "node:assert/strict";
import { onRequest } from "./define.ts";
import { serveHttp } from "./http.ts";
import { request } from "node:http";

const quiet = () => {};

test("requests and responses go through intact", async (t) => {
  const fn = onRequest(async (req) => {
    const body = req.method === "POST" ? await req.text() : "";
    const headers = new Headers({ "x-echo": req.headers.get("x-in") ?? "" });
    headers.append("set-cookie", "a=1");
    headers.append("set-cookie", "b=2");
    return new Response(`${req.method} ${new URL(req.url).pathname}${new URL(req.url).search} ${body}`, { status: 201, headers });
  });
  const s = await serveHttp(fn, 0, quiet);
  t.after(() => s.close(0));
  const res = await fetch(`http://127.0.0.1:${s.port}/a/b?c=1`, { method: "POST", body: "hello", headers: { "x-in": "yes" } });
  assert.equal(res.status, 201);
  assert.equal(await res.text(), "POST /a/b?c=1 hello");
  assert.equal(res.headers.get("x-echo"), "yes");
  assert.deepEqual(res.headers.getSetCookie(), ["a=1", "b=2"]);
});

test("the URL is the Host header with the forwarded scheme", async (t) => {
  const s = await serveHttp(onRequest((req) => new Response(req.url)), 0, quiet);
  t.after(() => s.close(0));
  const get = (headers: Record<string, string>) =>
    new Promise<string>((resolve, reject) => {
      request({ host: "127.0.0.1", port: s.port, path: "/x?y=1", headers }, (res) => {
        let body = "";
        res.on("data", (c) => (body += c)).on("end", () => resolve(body));
      }).on("error", reject).end();
    });
  assert.equal(await get({ host: "web--shop.gronblom.dev", "x-forwarded-proto": "https" }), "https://web--shop.gronblom.dev/x?y=1");
  // A client's X-Forwarded-Host reaches the app through Cloudflare and
  // Traefik: it mustn't decide the URL.
  assert.equal(
    await get({ host: "web--shop.gronblom.dev", "x-forwarded-proto": "https", "x-forwarded-host": "evil.example" }),
    "https://web--shop.gronblom.dev/x?y=1",
  );
  assert.equal(await get({ host: "paja.lankkupelit.fi", "x-forwarded-proto": "https,http" }), "https://paja.lankkupelit.fi/x?y=1");
  assert.equal(await get({ host: "localhost:8080" }), "http://localhost:8080/x?y=1");
  assert.equal(await get({ host: "web--shop.gronblom.dev", "x-forwarded-proto": "javascript" }), "http://web--shop.gronblom.dev/x?y=1");
});

test("a throwing handler gets a 500", async (t) => {
  const logs: string[] = [];
  const s = await serveHttp(onRequest(() => { throw new Error("boom"); }), 0, (m) => logs.push(m));
  t.after(() => s.close(0));
  const res = await fetch(`http://127.0.0.1:${s.port}/`);
  assert.equal(res.status, 500);
  assert.match(logs.join("\n"), /boom/);
});

test("close lets requests in flight finish", async () => {
  let release!: () => void;
  const gate = new Promise<void>((r) => (release = r));
  const s = await serveHttp(onRequest(async () => { await gate; return new Response("finished"); }), 0, quiet);
  const pending = fetch(`http://127.0.0.1:${s.port}/`).then((r) => r.text());
  await new Promise((r) => setTimeout(r, 50));
  const closed = s.close(5000);
  setTimeout(release, 100);
  assert.equal(await pending, "finished");
  await closed;
  await assert.rejects(fetch(`http://127.0.0.1:${s.port}/`));
});

test("close aborts what's still running after the grace period", async () => {
  let aborted = false;
  const s = await serveHttp(onRequest(async (_req, { signal }) => {
    await new Promise<void>((r) => signal.addEventListener("abort", () => { aborted = true; r(); }));
    return new Response("late");
  }), 0, quiet);
  const pending = fetch(`http://127.0.0.1:${s.port}/`).catch(() => "connection closed");
  await new Promise((r) => setTimeout(r, 50));
  await s.close(100);
  await pending;
  assert.equal(aborted, true);
});

// A Node listener (Express, Nitro's toNodeListener) gets the server's own
// request and response: chunks reach the client as they're written.
test("a Node listener streams, with the original Host", async (t) => {
  let release!: () => void;
  const gate = new Promise<void>((r) => (release = r));
  const fn = onRequest({ node: async (req, res) => {
    res.writeHead(200, { "content-type": "text/plain", "x-host": req.headers.host ?? "" });
    res.write("first\n");
    await gate;
    res.end("second\n");
  } });
  const s = await serveHttp(fn, 0, quiet);
  t.after(() => s.close(0));
  // node:http, not fetch: fetch won't send a Host of our choosing.
  const res = await new Promise<import("node:http").IncomingMessage>((resolve, reject) => {
    request({ host: "127.0.0.1", port: s.port, path: "/", headers: { host: "paja.lankkupelit.fi" } }, resolve).on("error", reject).end();
  });
  assert.equal(res.headers["x-host"], "paja.lankkupelit.fi");
  res.setEncoding("utf8");
  const chunks = res[Symbol.asyncIterator]();
  assert.equal((await chunks.next()).value, "first\n", "the first chunk arrives before the response ends");
  release();
  let rest = "";
  for (let r = await chunks.next(); !r.done; r = await chunks.next()) rest += r.value;
  assert.equal(rest, "second\n");
});

test("close waits for a Node listener's response to finish", async () => {
  const fn = onRequest({ node: (_req, res) => { setTimeout(() => res.end("done"), 100); } });
  const s = await serveHttp(fn, 0, quiet);
  const pending = fetch(`http://127.0.0.1:${s.port}/`).then((r) => r.text());
  await new Promise((r) => setTimeout(r, 20));
  await s.close(5_000);
  assert.equal(await pending, "done");
});

test("fetch() on a Node listener function", async () => {
  const fn = onRequest({ public: true }, { node: (req, res) => { res.setHeader("set-cookie", ["a=1", "b=2"]); res.end(`${req.method} ${req.url} ${req.headers.host}`); } });
  assert.equal(fn.options.public, true);
  assert.equal(typeof fn.node, "function");
  const res = await fn.fetch(new Request("http://web.example.com/x?y=1", { method: "POST", body: "b" }));
  assert.equal(await res.text(), "POST /x?y=1 web.example.com");
  assert.deepEqual(res.headers.getSetCookie(), ["a=1", "b=2"]);
});
