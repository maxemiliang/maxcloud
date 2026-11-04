// The runtime: loads the app's module, finds its functions, and either
// describes them (for mcloud, which turns them into manifests) or runs one.

import { readFileSync, existsSync } from "node:fs";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { isFunction, type MaxcloudFunction } from "./define.ts";
import { serveHttp } from "./http.ts";
import { backlogQuery, runWorker } from "./queue.ts";

/** A Kubernetes-safe name for an export: resizeImage -> resize-image. */
export function kubeName(exportName: string): string {
  const name = exportName
    .replace(/([a-z0-9])([A-Z])/g, "$1-$2")
    .replace(/_/g, "-")
    .toLowerCase();
  if (!/^[a-z]([-a-z0-9]{0,38}[a-z0-9])?$/.test(name)) {
    throw new Error(`export ${exportName}: its name (${name}) must start with a letter and be at most 40 letters, digits and dashes`);
  }
  return name;
}

export interface Found {
  name: string;
  exportName: string;
  fn: MaxcloudFunction;
}

export function findFunctions(mod: Record<string, unknown>): Found[] {
  const out: Found[] = [];
  const names = new Set<string>();
  for (const [exportName, value] of Object.entries(mod)) {
    if (!isFunction(value)) continue;
    if (exportName === "default") {
      throw new Error("a function is the default export; give it a named export instead, its name becomes the function's");
    }
    const name = kubeName(exportName);
    if (names.has(name)) throw new Error(`two exports turn into the function name ${name}`);
    names.add(name);
    out.push({ name, exportName, fn: value });
  }
  return out.sort((a, b) => a.name.localeCompare(b.name));
}

/** What `--describe` prints, with every default filled in. */
export function describe(found: Found[]) {
  return {
    sdk: "@max-cloud/functions",
    version: packageVersion(),
    functions: found.map(({ name, exportName, fn }) => {
      const base = { name, export: exportName, kind: fn.kind, memory: fn.options.memory ?? null, cpu: fn.options.cpu ?? null };
      switch (fn.kind) {
        case "http": {
          const o = fn.options;
          return { ...base, public: o.public ?? false, minInstances: o.minInstances ?? 0, maxInstances: o.maxInstances ?? 3, concurrency: o.concurrency ?? 0, timeoutSeconds: o.timeoutSeconds ?? 300 };
        }
        case "schedule": {
          const o = fn.options;
          return { ...base, schedule: o.schedule, timeZone: o.timeZone ?? "UTC", retries: o.retries ?? 0, timeoutSeconds: o.timeoutSeconds ?? 3600 };
        }
        case "queue": {
          const o = fn.options;
          return { ...base, queue: o.queue, concurrency: o.concurrency ?? 1, maxInstances: o.maxInstances ?? 3, maxAttempts: o.maxAttempts ?? 5, backoffSeconds: o.backoffSeconds ?? 10, timeoutSeconds: o.timeoutSeconds ?? 300, backlogQuery: backlogQuery(o.queue) };
        }
      }
    }),
  };
}

function packageVersion(): string {
  try {
    return JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8")).version;
  } catch {
    return "unknown";
  }
}

/** The app's entry module: --source, $FUNCTION_SOURCE, package.json's main, or index.js. */
export function sourcePath(source?: string, cwd = process.cwd()): string {
  const s = source ?? process.env.FUNCTION_SOURCE;
  if (s) return resolve(cwd, s);
  const pkgFile = resolve(cwd, "package.json");
  if (existsSync(pkgFile)) {
    const pkg = JSON.parse(readFileSync(pkgFile, "utf8"));
    if (typeof pkg.main === "string") return resolve(cwd, pkg.main);
  }
  return resolve(cwd, "index.js");
}

export async function load(path: string): Promise<Found[]> {
  const mod = await import(pathToFileURL(path).href);
  return findFunctions(mod);
}

export interface RunOptions {
  port?: number;
  /** Ends the run: HTTP drains, a worker stops, a schedule is aborted. */
  signal: AbortSignal;
  log?: (msg: string) => void;
}

/** Runs one function until it's done (schedule) or `signal` aborts. Resolves to the exit code. */
export async function run(f: Found, opts: RunOptions): Promise<number> {
  const log = opts.log ?? ((m: string) => console.log(m));
  const fn = f.fn;
  switch (fn.kind) {
    case "http": {
      const serving = await serveHttp(fn, opts.port ?? 8080, log);
      log(`${f.name}: listening on port ${serving.port}`);
      await new Promise<void>((r) => opts.signal.addEventListener("abort", () => r(), { once: true }));
      log(`${f.name}: shutting down`);
      await serving.close();
      return 0;
    }
    case "schedule": {
      const timeout = (fn.options.timeoutSeconds ?? 3600) * 1000;
      const signal = AbortSignal.any([opts.signal, AbortSignal.timeout(timeout)]);
      const startedAt = new Date();
      try {
        await Promise.race([
          fn.handler({ signal, startedAt }),
          new Promise((_, reject) => signal.addEventListener("abort", () => reject(signal.reason), { once: true })),
        ]);
        log(`${f.name}: done in ${((Date.now() - startedAt.getTime()) / 1000).toFixed(1)}s`);
        return 0;
      } catch (err) {
        log(`${f.name}: failed: ${err instanceof Error ? (err.stack ?? err.message) : String(err)}`);
        return 1;
      }
    }
    case "queue":
      await runWorker(fn, { signal: opts.signal, log });
      return 0;
  }
}
