import { test } from "node:test";
import assert from "node:assert/strict";
import { functionEntry, scheduledTasks, taskExport, wrapEntry } from "./entry.ts";

const paths = { env: "#maxcloud/env", functions: "/x/functions/dist/index.js" };

test("the env module comes first, and Nitro's runtime only behind a dynamic import", () => {
  const code = functionEntry(paths, { name: "web", public: true });
  const lines = code.split("\n");
  assert.equal(lines[0], 'import "#maxcloud/env";');
  // A static import of nitropack/runtime or h3 would create the app (and
  // read the runtimeConfig) before the env module ran.
  assert.doesNotMatch(code, /^import .*(nitropack|"h3")/m);
  assert.match(code, /await import\("nitropack\/runtime"\)/);
  assert.match(code, /if \(process\.env\.MAXCLOUD_DESCRIBE !== "1"\) await nitro\(\);/);
  assert.match(code, /export const web = onRequest\(\{"public":true\}, \{\n  node:/);
  assert.match(code, /start\(\{ web \}, \{ target: process\.env\.FUNCTION_TARGET \|\| "web" \}\)/);
});

test("scheduled tasks become scheduled functions with the app's container settings", () => {
  const code = functionEntry(paths, { name: "web", user: 1000, volumes: [{ path: "/scratch" }], minInstances: 1 }, {
    "0 3 * * *": ["db:cleanup", "report"],
    "*/5 * * * *": "report",
  });
  assert.match(code, /export const taskDbCleanup = onSchedule\(\{"schedule":"0 3 \* \* \*","user":1000,"volumes":\[\{"path":"\/scratch"\}\]\}/);
  assert.match(code, /export const taskReport2 = onSchedule\(\{"schedule":"\*\/5 \* \* \* \*"/);
  assert.doesNotMatch(code, /onSchedule\(\{[^)]*minInstances/, "HTTP-only options stay on the HTTP function");
  assert.match(code, /runTask\("db:cleanup"/);
  assert.match(code, /start\(\{ web, taskDbCleanup, taskReport, taskReport2 \}/);
  assert.doesNotMatch(functionEntry(paths, { tasks: false }, { "0 3 * * *": ["x"] }), /onSchedule\(\{/);
});

test("names", () => {
  assert.equal(taskExport("db:cleanup"), "taskDbCleanup");
  assert.equal(taskExport("nightly"), "taskNightly");
  assert.deepEqual(scheduledTasks(undefined), []);
  assert.match(functionEntry(paths, { name: "api-server" }), /export const apiServer = onRequest/);
});

test("wrapEntry maps the environment before Nitro's own entry", () => {
  const code = wrapEntry({ env: "#maxcloud/env" }, "/n/node-server.mjs");
  assert.equal(code.split("\n")[0], 'import "#maxcloud/env";');
  assert.match(code, /export \* from "\/n\/node-server\.mjs"/);
});
