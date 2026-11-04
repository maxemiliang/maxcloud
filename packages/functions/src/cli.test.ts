import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

const cli = fileURLToPath(new URL("./cli.ts", import.meta.url));
const app = fileURLToPath(new URL("./testdata/app.ts", import.meta.url));

function start(args: string[], env: Record<string, string> = {}) {
  const child = spawn(process.execPath, [cli, "--source", app, ...args], { env: { ...process.env, ...env } });
  let out = "";
  child.stdout.on("data", (d) => (out += d));
  child.stderr.on("data", (d) => (out += d));
  const exited = new Promise<number | null>((r) => child.on("exit", (code) => r(code)));
  return { child, exited, output: () => out };
}

test("--describe lists the functions", async () => {
  const p = start(["--describe"]);
  assert.equal(await p.exited, 0);
  const d = JSON.parse(p.output());
  assert.deepEqual(d.functions.map((f: { name: string; kind: string }) => `${f.name}:${f.kind}`), ["hello:http", "nightly-report:schedule", "resize:queue"]);
});

test("MAXCLOUD_DESCRIBE=1 prints one tagged line", async () => {
  const p = start([], { MAXCLOUD_DESCRIBE: "1", FUNCTION_TARGET: "hello" });
  assert.equal(await p.exited, 0);
  const line = p.output().split("\n").find((l) => l.startsWith("maxcloud:describe:"));
  assert.ok(line, p.output());
  assert.equal(JSON.parse(line.slice("maxcloud:describe:".length)).functions.length, 3);
});

test("an HTTP function finishes its request on SIGTERM and exits 0", async () => {
  const p = start(["--target", "hello", "--port", "0"]);
  const deadline = Date.now() + 5000;
  let port = 0;
  while (!port && Date.now() < deadline) {
    port = Number(/listening on port (\d+)/.exec(p.output())?.[1] ?? 0);
    await new Promise((r) => setTimeout(r, 20));
  }
  assert.ok(port, p.output());
  const slow = fetch(`http://127.0.0.1:${port}/?wait=300`).then((r) => r.text());
  await new Promise((r) => setTimeout(r, 100));
  p.child.kill("SIGTERM");
  assert.equal(await slow, "hello\n");
  assert.equal(await p.exited, 0, p.output());
});

test("a scheduled function exits 0, or 1 when it throws", async () => {
  assert.equal(await start([], { FUNCTION_TARGET: "nightly-report" }).exited, 0);
  const failing = start(["--target", "nightlyReport"], { FAIL: "1" });
  assert.equal(await failing.exited, 1);
  assert.match(failing.output(), /report failed/);
});

test("an unknown or missing target exits 2 and names the functions", async () => {
  const p = start(["--target", "nope"], { FUNCTION_TARGET: "" });
  assert.equal(await p.exited, 2);
  assert.match(p.output(), /hello, nightly-report, resize/);
});
