import { test } from "node:test";
import assert from "node:assert/strict";
import { onRequest } from "./define.ts";
import { serveHttp } from "./http.ts";

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

test("the URL uses the forwarded scheme and host", async (t) => {
  const s = await serveHttp(onRequest((req) => new Response(req.url)), 0, quiet);
  t.after(() => s.close(0));
  const res = await fetch(`http://127.0.0.1:${s.port}/x`, { headers: { "x-forwarded-proto": "https", "x-forwarded-host": "web-shop.gronblom.dev" } });
  assert.equal(await res.text(), "https://web-shop.gronblom.dev/x");
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
