import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

const app = fileURLToPath(new URL("./testdata/self-start.ts", import.meta.url));

function runApp(env: Record<string, string>, args: string[] = []) {
  return spawn(process.execPath, [app, ...args], { env: { ...process.env, ...env }, stdio: ["ignore", "pipe", "pipe"] });
}

async function output(p: ReturnType<typeof runApp>): Promise<{ code: number | null; out: string; err: string }> {
  let out = "", err = "";
  p.stdout!.on("data", (d) => (out += d));
  p.stderr!.on("data", (d) => (err += d));
  const code = await new Promise<number | null>((r) => p.on("exit", r));
  return { code, out, err };
}

test("a self-starting module describes itself as an image would be asked", async () => {
  const { code, out } = await output(runApp({ MAXCLOUD_DESCRIBE: "1" }));
  assert.equal(code, 0);
  const line = out.split("\n").find((l) => l.startsWith("maxcloud:describe:"))!;
  const d = JSON.parse(line.slice("maxcloud:describe:".length));
  assert.deepEqual(d.functions.map((f: { name: string }) => f.name), ["web"]);
  assert.doesNotMatch(out, /initialised/, "describing doesn't start the app");
});

test("a self-starting module serves its only function and runs shutdown hooks on SIGTERM", async () => {
  const p = runApp({ PORT: "0", MAXCLOUD_DESCRIBE: "" });
  const done = output(p);
  let port = 0;
  await new Promise<void>((resolve) => {
    p.stdout!.on("data", (d: Buffer) => {
      const m = /listening on port (\d+)/.exec(String(d));
      if (m) { port = Number(m[1]); resolve(); }
    });
  });
  const res = await fetch(`http://127.0.0.1:${port}/hi`);
  assert.equal(await res.text(), "web /hi");
  p.kill("SIGTERM");
  const { code, out } = await done;
  assert.equal(code, 0);
  assert.match(out, /shutdown hook ran/);
});
