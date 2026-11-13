// Defining functions: onRequest, onSchedule, onQueue. Each returns a plain
// object that the runtime recognises among the module's exports; nothing runs
// until the runtime picks one with FUNCTION_TARGET.

/** Marks an export as a maxcloud function. */
export const FUNCTION = Symbol.for("maxcloud.function");

/** Resources and limits every kind of function takes. */
export interface CommonOptions {
  /** Memory the container requests and is limited to, e.g. "256Mi". */
  memory?: string;
  /** CPU the container requests, e.g. "100m". */
  cpu?: string;
  /**
   * How long one invocation may run. HTTP: per request (default 300).
   * Schedule: the whole run (default 3600). Queue: per job (default 300).
   */
  timeoutSeconds?: number;
  /** Memory the container may use above `memory` (its request), for spikes, e.g. "1536Mi". */
  memoryLimit?: string;
  /** Run as this user id (and group), for an image whose Dockerfile has no non-root USER. */
  user?: number;
  /** Make the root filesystem read-only; write to `volumes` (and point TMPDIR at one). */
  readOnlyRootFilesystem?: boolean;
  /**
   * Scratch directories, empty at every start. `memory: true` keeps one in
   * RAM (counted against the container's memory): a bigger /dev/shm for
   * Chromium, say. Knative doesn't allow /tmp.
   */
  volumes?: Volume[];
}

export interface HttpOptions extends CommonOptions {
  /** Reachable from the internet at https://<name>--<app>.makiya.dev. Default false: cluster-local only. */
  public?: boolean;
  /** Instances kept running when idle. Default 0 (scale to zero). */
  minInstances?: number;
  /** Most instances at once. Default 3. */
  maxInstances?: number;
  /** Requests one instance handles at once before another starts. Default 0 (no limit; Knative scales on 100 in flight). */
  concurrency?: number;
  /**
   * Put the public hostname behind Cloudflare (its DNS record is proxied):
   * WAF, caching, Cloudflare Access. Needs `public`. Default false.
   */
  proxied?: boolean;
  /**
   * A path that answers 2xx when an instance can take requests: it gets
   * traffic only after that, and is restarted when it stops answering.
   * Default: the port accepting connections is enough.
   */
  healthPath?: string;
}

export interface Volume {
  /** Where it's mounted, e.g. "/scratch" or "/dev/shm". */
  path: string;
  /** e.g. "1Gi". */
  sizeLimit?: string;
  /** In RAM (tmpfs) instead of on the node's disk. */
  memory?: boolean;
}

export interface ScheduleOptions extends CommonOptions {
  /** Cron schedule, e.g. "0 3 * * *". */
  schedule: string;
  /** IANA time zone for the schedule. Default UTC. */
  timeZone?: string;
  /** Times a failed run is retried. Default 0. */
  retries?: number;
}

export interface QueueOptions extends CommonOptions {
  /** Queue name: lowercase letters, digits, dashes and underscores. */
  queue: string;
  /** Jobs one instance works on at once. Default 1. */
  concurrency?: number;
  /** Most instances at once. Default 3. */
  maxInstances?: number;
  /** Tries before a job is marked failed. Default 5. */
  maxAttempts?: number;
  /** Delay before the first retry, doubled for each later one. Default 10. */
  backoffSeconds?: number;
}

export interface HttpContext {
  /** Aborted when the client goes away or the server shuts down. */
  signal: AbortSignal;
}

export type HttpHandler = (request: Request, context: HttpContext) => Response | Promise<Response>;

/**
 * A Node.js request listener: Express, Fastify's `server.routing`, h3's
 * `toNodeListener(app)`, Nitro. It gets the server's own request and
 * response, so streaming and the original headers work as they do in a
 * plain Node server.
 */
export type NodeListener = (req: import("node:http").IncomingMessage, res: import("node:http").ServerResponse) => unknown;

/** `{ node: listener }`: an HTTP function served by a Node.js request listener. */
export interface NodeHandler {
  node: NodeListener;
}

export interface ScheduleContext {
  /** Aborted at the timeout, or when the run is stopped. */
  signal: AbortSignal;
  /** When this run started. */
  startedAt: Date;
}

export type ScheduleHandler = (context: ScheduleContext) => void | Promise<void>;

export interface Job<T> {
  id: string;
  queue: string;
  payload: T;
  /** 1 on the first try. */
  attempt: number;
  createdAt: Date;
}

export interface JobContext {
  /** Aborted at the timeout, or when the worker shuts down. */
  signal: AbortSignal;
}

export type JobHandler<T> = (job: Job<T>, context: JobContext) => void | Promise<void>;

export interface HttpFunction {
  readonly [FUNCTION]: true;
  readonly kind: "http";
  readonly options: HttpOptions;
  /** The fetch-style handler; for a Node listener, one that goes through a local server. */
  readonly handler: HttpHandler;
  /** Set for a function made from a Node.js request listener (`{ node }`): served directly. */
  readonly node?: NodeListener;
  /** Calls the handler directly, e.g. in tests. */
  fetch(request: Request): Promise<Response>;
}

export interface ScheduleFunction {
  readonly [FUNCTION]: true;
  readonly kind: "schedule";
  readonly options: ScheduleOptions;
  readonly handler: ScheduleHandler;
}

export interface QueueFunction<T = unknown> {
  readonly [FUNCTION]: true;
  readonly kind: "queue";
  readonly options: QueueOptions;
  readonly handler: JobHandler<T>;
}

export type MaxcloudFunction = HttpFunction | ScheduleFunction | QueueFunction<any>;

export function isFunction(v: unknown): v is MaxcloudFunction {
  return typeof v === "object" && v !== null && FUNCTION in v;
}

/**
 * An HTTP function: a Knative service that scales to zero.
 *
 * ```ts
 * export const hello = onRequest(async (req) => new Response("hi"));
 * export const api = onRequest({ public: true }, app.fetch); // e.g. a Hono app
 * ```
 */
export function onRequest(handler: HttpHandler | NodeHandler): HttpFunction;
export function onRequest(options: HttpOptions, handler: HttpHandler | NodeHandler): HttpFunction;
export function onRequest(a: HttpOptions | HttpHandler | NodeHandler, b?: HttpHandler | NodeHandler): HttpFunction {
  const [options, h] = typeof a === "function" || isNodeHandler(a) ? [{}, a] : [a as HttpOptions, b];
  if (isNodeHandler(h)) {
    const node = h.node;
    const handler: HttpHandler = (request) => fetchThroughNode(node, request);
    return { [FUNCTION]: true, kind: "http", options, handler, node, fetch: (request) => fetchThroughNode(node, request) };
  }
  if (typeof h !== "function") throw new TypeError("onRequest: missing handler (a function, or { node: listener })");
  const handler = h;
  return {
    [FUNCTION]: true,
    kind: "http",
    options,
    handler,
    async fetch(request) {
      return handler(request, { signal: request.signal });
    },
  };
}

function isNodeHandler(v: unknown): v is NodeHandler {
  return typeof v === "object" && v !== null && typeof (v as NodeHandler).node === "function";
}

/** Sends one request through a Node listener on a throwaway local server (for fetch() in tests). */
async function fetchThroughNode(node: NodeListener, request: Request): Promise<Response> {
  const http = await import("node:http");
  const server = http.createServer((req, res) => void node(req, res));
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", () => r()));
  const { port } = server.address() as import("node:net").AddressInfo;
  const url = new URL(request.url);
  const headers: Record<string, string> = {};
  request.headers.forEach((v, k) => (headers[k] = v));
  headers.host = url.host; // node:http, not fetch: fetch drops the Host it's given
  const body = request.body ? Buffer.from(await request.arrayBuffer()) : undefined;
  try {
    return await new Promise<Response>((resolve, reject) => {
      const req = http.request({ host: "127.0.0.1", port, method: request.method, path: url.pathname + url.search, headers }, (res) => {
        const chunks: Buffer[] = [];
        res.on("data", (c: Buffer) => chunks.push(c));
        res.on("error", reject);
        res.on("end", () => {
          const h = new Headers();
          for (const [k, v] of Object.entries(res.headers)) {
            for (const x of Array.isArray(v) ? v : v === undefined ? [] : [v]) h.append(k, x);
          }
          const empty = request.method === "HEAD" || [204, 304].includes(res.statusCode ?? 200);
          resolve(new Response(empty ? null : Buffer.concat(chunks), { status: res.statusCode, statusText: res.statusMessage, headers: h }));
        });
      });
      req.on("error", reject);
      req.end(body);
    });
  } finally {
    server.closeAllConnections();
    await new Promise((r) => server.close(r));
  }
}

/**
 * A scheduled function: a Kubernetes CronJob that runs it once per tick.
 *
 * ```ts
 * export const nightly = onSchedule("0 3 * * *", async () => { ... });
 * ```
 */
export function onSchedule(schedule: string, handler: ScheduleHandler): ScheduleFunction;
export function onSchedule(options: ScheduleOptions, handler: ScheduleHandler): ScheduleFunction;
export function onSchedule(a: string | ScheduleOptions, handler: ScheduleHandler): ScheduleFunction {
  const options = typeof a === "string" ? { schedule: a } : a;
  if (!options.schedule || options.schedule.trim().split(/\s+/).length !== 5) {
    throw new TypeError(`onSchedule: ${JSON.stringify(options.schedule)} isn't a five-field cron schedule`);
  }
  if (typeof handler !== "function") throw new TypeError("onSchedule: missing handler");
  return { [FUNCTION]: true, kind: "schedule", options, handler };
}

const QUEUE_NAME = /^[a-z0-9][a-z0-9_-]{0,62}$/;

export function checkQueueName(queue: string): void {
  if (!QUEUE_NAME.test(queue)) {
    throw new TypeError(`queue name ${JSON.stringify(queue)}: use lowercase letters, digits, dashes and underscores`);
  }
}

/**
 * A queue function: a worker that KEDA starts when the queue has jobs, and
 * scales to zero when it's empty. Add jobs with `enqueue`.
 *
 * ```ts
 * export const resize = onQueue<{ key: string }>("images", async (job) => { ... });
 * ```
 *
 * A job that throws is retried with backoff; one still running when its
 * timeout passes is aborted and retried too. Make handlers safe to run twice.
 */
export function onQueue<T = unknown>(queue: string, handler: JobHandler<T>): QueueFunction<T>;
export function onQueue<T = unknown>(options: QueueOptions, handler: JobHandler<T>): QueueFunction<T>;
export function onQueue<T = unknown>(a: string | QueueOptions, handler: JobHandler<T>): QueueFunction<T> {
  const options = typeof a === "string" ? { queue: a } : a;
  checkQueueName(options.queue);
  if (typeof handler !== "function") throw new TypeError("onQueue: missing handler");
  return { [FUNCTION]: true, kind: "queue", options, handler };
}
