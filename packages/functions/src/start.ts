// start: the runtime for a module that is its own entrypoint. What
// maxcloud-functions does for package.json's main, a bundled server (a
// Nitro build, say) does for itself when it's run with `node`:
//
//   MAXCLOUD_DESCRIBE=1 node server.mjs   prints the functions (as mcloud asks an image)
//   FUNCTION_TARGET=web node server.mjs   runs that function
//
// It exits the process when the function is done (SIGTERM for HTTP).

import { migrate } from "./migrate.ts";
import { DESCRIBE_TAG, describe, findFunctions, run, type Found } from "./runtime.ts";

export interface StartOptions {
  /** Default $FUNCTION_TARGET (for start(), else the only function there is). */
  target?: string;
  /** Default $PORT, else 8080. */
  port?: number;
}

/** Stops on SIGTERM (how Kubernetes stops a pod) or SIGINT. */
export function stopSignal(): AbortSignal {
  const stop = new AbortController();
  for (const sig of ["SIGTERM", "SIGINT"] as const) {
    process.once(sig, () => stop.abort(new Error(`${sig} received`)));
  }
  return stop.signal;
}

/**
 * Describes or runs the functions among `found`, as the container's
 * entrypoint does. Resolves to the exit code.
 */
export async function runFound(found: Found[], opts: StartOptions & { describe?: "json" | "tagged"; onlyByDefault?: boolean } = {}): Promise<number> {
  if (opts.describe === "json") {
    process.stdout.write(JSON.stringify(describe(found), null, 2) + "\n");
    return 0;
  }
  if (opts.describe === "tagged") {
    process.stdout.write(DESCRIBE_TAG + JSON.stringify(describe(found)) + "\n");
    return 0;
  }
  const names = found.map((f) => f.name).join(", ") || "none";
  const target = opts.target ?? process.env.FUNCTION_TARGET ?? (opts.onlyByDefault && found.length === 1 ? found[0]!.name : undefined);
  if (!target) {
    console.error(`maxcloud-functions: which function? Set FUNCTION_TARGET or --target (${names})`);
    return 2;
  }
  const f = found.find((x) => x.name === target || x.exportName === target);
  if (!f) {
    console.error(`maxcloud-functions: no function ${target} (found: ${names})`);
    return 2;
  }
  // Apply the app's pending migrations first: an HTTP function isn't
  // ready (and a new revision takes no traffic) until the schema is what
  // its code expects. Dev mode leaves them to mcloud dev.
  if (process.env.MAXCLOUD_MIGRATE !== "0") {
    try {
      const applied = await migrate({ log: (m) => console.log(`migrations: ${m}`) });
      if (applied.length > 0) console.log(`migrations: applied ${applied.length}`);
    } catch (err) {
      console.error(`maxcloud-functions: migrations: ${err instanceof Error ? err.message : err}`);
      return 1;
    }
  }

  // Finish cleanly and exit 0, so pods don't end in Error.
  const port = opts.port ?? Number(process.env.PORT ?? 8080);
  const code = await run(f, { port, signal: stopSignal() });
  return (await runShutdownHooks()) ? code : code || 1;
}

// On globalThis: a bundled app (a Nitro build) has its own copy of this
// package, and maxcloud-functions, which loads it, must run its hooks too.
const HOOKS = Symbol.for("maxcloud.shutdownHooks");
const shutdownHooks: (() => unknown)[] = ((globalThis as Record<symbol, unknown>)[HOOKS] as (() => unknown)[] | undefined) ??
  ((globalThis as Record<symbol, unknown>)[HOOKS] = []);

/**
 * Runs `fn` when the function stops (after SIGTERM, once the requests or
 * jobs in flight are done), before the process exits: close pools, flush
 * logs. Hooks run in order, for at most 10 seconds together.
 */
export function onShutdown(fn: () => unknown): void {
  shutdownHooks.push(fn);
}

/** Runs the hooks; false if one failed or they took too long. */
export async function runShutdownHooks(timeoutMs = 10_000): Promise<boolean> {
  const hooks = shutdownHooks.splice(0);
  let ok = true;
  const all = (async () => {
    for (const h of hooks) {
      try {
        await h();
      } catch (err) {
        ok = false;
        console.error("maxcloud-functions: shutdown hook failed:", err instanceof Error ? (err.stack ?? err.message) : err);
      }
    }
  })();
  const timedOut = await Promise.race([all.then(() => false), new Promise<boolean>((r) => setTimeout(() => r(true), timeoutMs).unref())]);
  if (timedOut) console.error(`maxcloud-functions: shutdown hooks still running after ${timeoutMs / 1000}s; exiting`);
  return ok && !timedOut;
}

/**
 * Runs the module's functions as its own entrypoint, and exits:
 *
 * ```ts
 * export const web = onRequest({ public: true }, { node: listener });
 * if (await isEntrypoint(import.meta.url)) start({ web });
 * ```
 */
export function start(mod: Record<string, unknown>, opts: StartOptions = {}): void {
  const describing = process.env.MAXCLOUD_DESCRIBE === "1" ? "tagged" : process.argv.includes("--describe") ? "json" : undefined;
  runFound(findFunctions(mod), { ...opts, describe: describing, onlyByDefault: true }).then(
    (code) => process.exit(code),
    (err) => {
      console.error("maxcloud-functions:", err instanceof Error ? (err.stack ?? err.message) : err);
      process.exit(1);
    },
  );
}

/** Whether the module at `url` (its import.meta.url) is the one node was started with. */
export async function isEntrypoint(url: string): Promise<boolean> {
  const arg = process.argv[1];
  if (!arg) return false;
  const { realpath } = await import("node:fs/promises");
  const { fileURLToPath } = await import("node:url");
  try {
    return (await realpath(fileURLToPath(url))) === (await realpath(arg));
  } catch {
    return false;
  }
}
