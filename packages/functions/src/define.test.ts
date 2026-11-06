import { test } from "node:test";
import assert from "node:assert/strict";
import { onQueue, onRequest, onSchedule } from "./define.ts";
import { describe, findFunctions, kubeName } from "./runtime.ts";

test("onRequest calls through fetch", async () => {
  const fn = onRequest({ public: true }, (req) => new Response(`hi ${new URL(req.url).pathname}`));
  const res = await fn.fetch(new Request("http://x/there"));
  assert.equal(await res.text(), "hi /there");
  assert.equal(fn.options.public, true);
});

test("onSchedule wants five cron fields", () => {
  assert.throws(() => onSchedule("every day", async () => {}), /five-field/);
  assert.equal(onSchedule("0 3 * * *", async () => {}).options.schedule, "0 3 * * *");
});

test("onQueue checks the queue name", () => {
  assert.throws(() => onQueue("Images!", async () => {}), /queue name/);
  assert.equal(onQueue({ queue: "images", concurrency: 2 }, async () => {}).options.concurrency, 2);
});

test("export names become Kubernetes names", () => {
  assert.equal(kubeName("resizeImage"), "resize-image");
  assert.equal(kubeName("send_mail"), "send-mail");
  assert.equal(kubeName("APIHandler"), "apihandler");
  assert.throws(() => kubeName("x".repeat(41)));
  assert.throws(() => kubeName("2fast"));
});

test("findFunctions ignores other exports and rejects a default", () => {
  const hello = onRequest(() => new Response("hi"));
  const found = findFunctions({ hello, helper: () => 1, VERSION: "1" });
  assert.deepEqual(found.map((f) => f.name), ["hello"]);
  assert.throws(() => findFunctions({ default: hello }), /default export/);
  assert.throws(() => findFunctions({ fooBar: hello, foo_bar: hello }), /two exports/);
});

test("describe passes an HTTP function's container options on", () => {
  const d = describe(
    findFunctions({
      web: onRequest(
        {
          public: true, proxied: true, memory: "384Mi", memoryLimit: "1536Mi", user: 1000, readOnlyRootFilesystem: true,
          volumes: [{ path: "/scratch", sizeLimit: "1Gi" }, { path: "/dev/shm", memory: true }], healthPath: "/login",
        },
        () => new Response(),
      ),
    }),
  );
  assert.deepEqual(d.functions[0], {
    name: "web", export: "web", kind: "http", memory: "384Mi", cpu: null,
    public: true, minInstances: 0, maxInstances: 3, concurrency: 0, timeoutSeconds: 300,
    proxied: true, memoryLimit: "1536Mi", user: 1000, readOnlyRootFilesystem: true,
    volumes: [{ path: "/scratch", sizeLimit: "1Gi", memory: false }, { path: "/dev/shm", sizeLimit: "", memory: true }],
    healthPath: "/login",
  });
});

test("describe fills in the defaults", () => {
  const d = describe(
    findFunctions({
      api: onRequest({ public: true, memory: "256Mi" }, () => new Response()),
      nightly: onSchedule("0 3 * * *", async () => {}),
      resizeImage: onQueue("images", async () => {}),
    }),
  );
  assert.equal(d.sdk, "@max-cloud/functions");
  assert.match(d.version, /^\d+\.\d+\.\d+/);
  assert.deepEqual(d.functions[0], {
    name: "api", export: "api", kind: "http", memory: "256Mi", cpu: null,
    public: true, minInstances: 0, maxInstances: 3, concurrency: 0, timeoutSeconds: 300,
    proxied: false, memoryLimit: null, user: null, readOnlyRootFilesystem: false, volumes: [], healthPath: "",
  });
  assert.deepEqual(d.functions[1], {
    name: "nightly", export: "nightly", kind: "schedule", memory: null, cpu: null,
    schedule: "0 3 * * *", timeZone: "UTC", retries: 0, timeoutSeconds: 3600,
    memoryLimit: null, user: null, readOnlyRootFilesystem: false, volumes: [],
  });
  const q = d.functions[2]!;
  assert.equal(q.name, "resize-image");
  assert.equal(q.kind, "queue");
  assert.match((q as { backlogQuery: string }).backlogQuery, /queue = 'images'/);
});
