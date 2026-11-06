// The fixture (test/fixture, shaped like lankkupaja) built and run as a
// maxcloud function: what mcloud asks the image, and how it serves.
// Builds with Nuxt first (a few seconds); MAXCLOUD_SKIP_NUXT_BUILD=1 reuses
// the last build.

import { before, test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { request } from "node:http";
import { fileURLToPath } from "node:url";

const fixture = fileURLToPath(new URL("../test/fixture/", import.meta.url));
const server = ".output/server/index.mjs";

before(() => {
  if (process.env.MAXCLOUD_SKIP_NUXT_BUILD !== "1") {
    execFileSync("npx", ["--no-install", "nuxt", "build"], { cwd: fixture, stdio: "ignore" });
  }
});

const maxcloudEnv = {
  MAXCLOUD_APP: "nuxt-demo",
  DATABASE_URL: "postgresql://app:pw@db-rw:5432/app",
  AWS_ACCESS_KEY_ID: "KEY",
  AWS_SECRET_ACCESS_KEY: "SECRET",
  AWS_ENDPOINT_URL_S3: "https://s3.example.com",
  AWS_REGION: "europe-1",
  MAXCLOUD_BUCKET_UPLOADS: "mc-nuxt-demo--uploads",
};

function run(env: Record<string, string>) {
  const p = spawn(process.execPath, [server], { cwd: fixture, env: { PATH: process.env.PATH!, ...env }, stdio: ["ignore", "pipe", "pipe"] });
  let out = "";
  p.stdout!.on("data", (d) => (out += d));
  p.stderr!.on("data", (d) => (out += d));
  const exited = new Promise<number | null>((r) => p.on("exit", r));
  return { p, exited, output: () => out };
}

function get(port: number, path: string, headers: Record<string, string> = {}): Promise<{ status: number; body: string; times: number[] }> {
  return new Promise((resolve, reject) => {
    request({ host: "127.0.0.1", port, path, headers }, (res) => {
      let body = "";
      const times: number[] = [];
      res.setEncoding("utf8");
      res.on("data", (d) => { body += d; times.push(Date.now()); });
      res.on("end", () => resolve({ status: res.statusCode ?? 0, body, times }));
    }).on("error", reject).end();
  });
}

async function listening(app: ReturnType<typeof run>): Promise<number> {
  for (let i = 0; i < 100; i++) {
    const m = /web: listening on port (\d+)/.exec(app.output());
    if (m) return Number(m[1]);
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error("didn't start: " + app.output());
}

test("describing the image doesn't start the app", async () => {
  const app = run({ MAXCLOUD_DESCRIBE: "1" });
  assert.equal(await app.exited, 0);
  const line = app.output().split("\n").find((l) => l.startsWith("maxcloud:describe:"))!;
  const d = JSON.parse(line.slice("maxcloud:describe:".length));
  const web = d.functions.find((f: { name: string }) => f.name === "web");
  assert.equal(web.kind, "http");
  assert.equal(web.healthPath, "/api/health");
  assert.deepEqual(web.volumes.map((v: { path: string }) => v.path), ["/scratch", "/dev/shm"]);
  const task = d.functions.find((f: { name: string }) => f.name === "task-cleanup");
  assert.equal(task.schedule, "0 3 * * *");
  assert.equal(task.user, 1000, "the task gets the image's user too");
  assert.doesNotMatch(app.output(), /plugin started/);
});

test("the app as a function: config, Host, assets, streaming, shutdown", async () => {
  const app = run({ ...maxcloudEnv, PORT: "0", FUNCTION_TARGET: "web" });
  const port = await listening(app);
  assert.match(app.output(), /plugin started[\s\S]*web: listening/, "plugins run before it takes requests");

  const config = JSON.parse((await get(port, "/api/config")).body);
  assert.deepEqual(config, {
    databaseUrl: maxcloudEnv.DATABASE_URL,
    bucket: "mc-nuxt-demo--uploads",
    endpoint: "https://s3.example.com",
    hasKey: true,
    maxcloud: { app: "nuxt-demo", buckets: { uploads: "mc-nuxt-demo--uploads" }, hasDatabase: true },
  });

  const host = JSON.parse((await get(port, "/api/host", { host: "paja.lankkupelit.fi", "x-forwarded-proto": "https" })).body);
  assert.deepEqual(host, { host: "paja.lankkupelit.fi", url: "https://paja.lankkupelit.fi/api/host" });

  const page = await get(port, "/", { host: "paja.lankkupelit.fi" });
  assert.match(page.body, /<p id="host">paja\.lankkupelit\.fi<\/p>/, "server-rendered with the request's Host");
  const asset = /\/_nuxt\/[^"]+\.js/.exec(page.body)![0];
  assert.equal((await get(port, asset)).status, 200);
  assert.equal((await get(port, "/hello.txt")).body, "a static file\n");

  const stream = await get(port, "/api/stream");
  assert.equal(stream.body.split("\n").filter(Boolean).length, 3);
  assert.ok(stream.times.length >= 3 && stream.times.at(-1)! - stream.times[0]! >= 500, `chunks arrived as written (${stream.times})`);

  app.p.kill("SIGTERM");
  assert.equal(await app.exited, 0);
  assert.match(app.output(), /plugin closed/, "Nitro's close hook ran");
});

test("a scheduled task runs once, as its CronJob would", async () => {
  const app = run({ ...maxcloudEnv, FUNCTION_TARGET: "task-cleanup" });
  assert.equal(await app.exited, 0);
  assert.match(app.output(), /cleanup ran number[\s\S]*task cleanup: "cleaned"[\s\S]*plugin closed/);
});
