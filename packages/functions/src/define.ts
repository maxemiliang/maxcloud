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
}

export interface HttpOptions extends CommonOptions {
  /** Reachable from the internet at https://<name>-<app>.gronblom.dev. Default false: cluster-local only. */
  public?: boolean;
  /** Instances kept running when idle. Default 0 (scale to zero). */
  minInstances?: number;
  /** Most instances at once. Default 3. */
  maxInstances?: number;
  /** Requests one instance handles at once before another starts. Default 0 (no limit; Knative scales on 100 in flight). */
  concurrency?: number;
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
  readonly handler: HttpHandler;
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
export function onRequest(handler: HttpHandler): HttpFunction;
export function onRequest(options: HttpOptions, handler: HttpHandler): HttpFunction;
export function onRequest(a: HttpOptions | HttpHandler, b?: HttpHandler): HttpFunction {
  const [options, handler] = typeof a === "function" ? [{}, a] : [a, b];
  if (typeof handler !== "function") throw new TypeError("onRequest: missing handler");
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
