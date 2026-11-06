// Dev mode: the whole app in one process, for local development
// (`maxcloud-functions dev`, which `mcloud dev` runs). One HTTP server on
// 127.0.0.1 routes <name>.localhost to the HTTP function deployed as <name>;
// on localhost itself, GET / lists the functions and POST /_run/<name> runs a
// schedule once. Queue workers run in-process when DATABASE_URL is set.
// Schedules never run on their cron here.

import type { HttpContext, HttpFunction, QueueFunction, ScheduleFunction } from "./define.ts";
import { serveHttp } from "./http.ts";
import { runWorker } from "./queue.ts";
import { errorText, runScheduleLogged, type Found } from "./runtime.ts";

export interface DevOptions {
  /** 0 picks a free port. */
  port: number;
  /** Stops dev mode: requests and jobs in flight finish first. */
  signal: AbortSignal;
  /** Where the functions' logs go. Default console.log. */
  log?: (msg: string) => void;
  /** Where the startup table goes. Default stderr. */
  print?: (msg: string) => void;
}

export interface DevServer {
  /** The port it listens on. */
  port: number;
  /** Resolves once dev mode has stopped. */
  stopped: Promise<void>;
}

/** One entry in GET /'s listing. */
export interface DevListing {
  kind: "http" | "schedule" | "queue";
  name: string;
  url?: string;
}

const RUN_PATH = /^\/_run\/([^/]+)$/;

/** The hostname of a Host header, lowercase and without the port. */
export function hostOnly(host: string | null | undefined): string {
  const h = (host ?? "").trim().toLowerCase();
  if (h.startsWith("[")) return h.slice(0, h.indexOf("]") + 1);
  const colon = h.lastIndexOf(":");
  return colon === -1 ? h : h.slice(0, colon);
}

function text(status: number, body: string): Response {
  return new Response(body + "\n", { status, headers: { "content-type": "text/plain; charset=utf-8" } });
}

/** Starts dev mode for the app's functions. */
export async function startDev(found: Found[], opts: DevOptions): Promise<DevServer> {
  const log = opts.log ?? ((m: string) => console.log(m));
  const print = opts.print ?? ((m: string) => process.stderr.write(m + "\n"));
  const http = new Map<string, HttpFunction>();
  const schedules = new Map<string, ScheduleFunction>();
  const queues: { name: string; fn: QueueFunction<any> }[] = [];
  for (const { name, fn } of found) {
    if (fn.kind === "http") http.set(name, fn);
    else if (fn.kind === "schedule") schedules.set(name, fn);
    else queues.push({ name, fn });
  }

  let port = opts.port;
  const root = () => `http://localhost:${port}/`;
  const listing = (): DevListing[] =>
    found.map(({ name, fn }) => (fn.kind === "http" ? { kind: fn.kind, name, url: `http://${name}.localhost:${port}/` } : { kind: fn.kind, name }));
  const notFound = () => text(404, `not found: GET ${root()} lists the functions, POST ${root()}_run/<name> runs a scheduled one`);

  const handler = async (req: Request, ctx: HttpContext): Promise<Response> => {
    const host = hostOnly(req.headers.get("host"));
    if (host.endsWith(".localhost")) {
      const name = host.slice(0, -".localhost".length);
      const fn = http.get(name);
      if (!fn) return text(404, `no HTTP function ${name}: GET ${root()} lists the functions`);
      return fn.handler(req, ctx);
    }
    const path = new URL(req.url).pathname;
    if (req.method === "GET" && path === "/") return Response.json(listing());
    const run = RUN_PATH.exec(path);
    if (req.method === "POST" && run) {
      const name = run[1]!;
      const fn = schedules.get(name);
      if (!fn) return text(404, `no scheduled function ${name}: GET ${root()} lists the functions`);
      const err = await runScheduleLogged(name, fn, ctx.signal, log);
      if (err === undefined) return text(200, "ok");
      return text(500, err instanceof Error ? err.message : String(err));
    }
    return notFound();
  };

  // <name>.localhost for a function made from a Node listener goes to it directly.
  const nodeFor = (req: import("node:http").IncomingMessage) => {
    const host = hostOnly(req.headers.host);
    return host.endsWith(".localhost") ? http.get(host.slice(0, -".localhost".length))?.node : undefined;
  };
  const serving = await serveHttp({ handler, nodeFor }, opts.port, log, "127.0.0.1");
  port = serving.port;

  const hasDatabase = Boolean(process.env.DATABASE_URL);
  const workers = hasDatabase
    ? queues.map(({ name, fn }) =>
        runWorker(fn, { signal: opts.signal, log }).catch((err) => log(`${name}: worker stopped: ${errorText(err)}`)),
      )
    : [];

  print(devTable(found, port, hasDatabase));

  const stopped = (async () => {
    await new Promise<void>((r) => {
      if (opts.signal.aborted) r();
      else opts.signal.addEventListener("abort", () => r(), { once: true });
    });
    log("maxcloud dev: shutting down");
    await Promise.all([serving.close(), ...workers]);
  })();
  return { port, stopped };
}

/** The startup table: one row per function, and how to reach it. */
export function devTable(found: Found[], port: number, workersRunning: boolean): string {
  const rows = found.map(({ name, fn }) => {
    switch (fn.kind) {
      case "http":
        return [fn.kind, name, `http://${name}.localhost:${port}/`];
      case "schedule":
        return [fn.kind, name, `POST http://localhost:${port}/_run/${name}`];
      case "queue":
        return [fn.kind, name, workersRunning ? "worker running" : "not running (no DATABASE_URL)"];
    }
  });
  const kindWidth = Math.max(0, ...rows.map((r) => r[0]!.length));
  const nameWidth = Math.max(0, ...rows.map((r) => r[1]!.length));
  const lines = [`maxcloud dev: http://localhost:${port}/ lists the functions`];
  if (rows.length === 0) lines.push("  (no functions found)");
  for (const [kind, name, where] of rows) lines.push(`  ${kind!.padEnd(kindWidth)}  ${name!.padEnd(nameWidth)}  ${where}`);
  if (!workersRunning && found.some((f) => f.fn.kind === "queue")) {
    lines.push("queue workers don't run without DATABASE_URL (mcloud dev provides one)");
  }
  return lines.join("\n");
}
