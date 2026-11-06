#!/usr/bin/env node
// maxcloud-functions: the container's entrypoint.
//
//   maxcloud-functions                 run $FUNCTION_TARGET
//   maxcloud-functions --target hello  run that function
//   maxcloud-functions --describe      print the app's functions as JSON
//   maxcloud-functions dev             run the whole app locally
//   MAXCLOUD_DESCRIBE=1 maxcloud-functions   the same, as one tagged line

import { parseArgs } from "node:util";
import { startDev } from "./dev.ts";
import { DESCRIBE_TAG, describe, devEntryPath, load, run, sourcePath } from "./runtime.ts";

const usage = `usage: maxcloud-functions [--target NAME] [--source FILE] [--port N]
       maxcloud-functions --describe [--source FILE]
       maxcloud-functions dev [--entry FILE] [--port N]

Runs one of the app's functions: the HTTP server, a scheduled run, or a queue
worker. NAME defaults to $FUNCTION_TARGET, FILE to package.json's "main", the
port to $PORT or 8080.

dev runs the whole app on 127.0.0.1: http://<name>.localhost:<port>/ reaches
an HTTP function, GET / lists them, POST /_run/<name> runs a schedule once,
and queue workers run when DATABASE_URL is set. FILE defaults to src/index.ts
if it exists, else package.json's "main".`;

/** Stops on SIGTERM (how Kubernetes stops a pod) or SIGINT. */
function stopSignal(): AbortSignal {
  const stop = new AbortController();
  for (const sig of ["SIGTERM", "SIGINT"] as const) {
    process.once(sig, () => stop.abort(new Error(`${sig} received`)));
  }
  return stop.signal;
}

async function dev(args: string[]): Promise<number> {
  const { values } = parseArgs({
    args,
    options: {
      entry: { type: "string" },
      port: { type: "string" },
      help: { type: "boolean", short: "h" },
    },
  });
  if (values.help) {
    console.log(usage);
    return 0;
  }
  process.env.MAXCLOUD_DEV = "1";
  const found = await load(devEntryPath(values.entry));
  const port = Number(values.port ?? process.env.PORT ?? 8080);
  const server = await startDev(found, { port, signal: stopSignal() });
  await server.stopped;
  return 0;
}

async function main(): Promise<number> {
  if (process.argv[2] === "dev") return dev(process.argv.slice(3));
  const { values } = parseArgs({
    options: {
      target: { type: "string" },
      source: { type: "string" },
      port: { type: "string" },
      describe: { type: "boolean" },
      help: { type: "boolean", short: "h" },
    },
  });
  if (values.help) {
    console.log(usage);
    return 0;
  }
  // MAXCLOUD_DESCRIBE=1 in the environment does the same as --describe,
  // as one tagged line: that's how the deploy workflow asks a built image,
  // whatever its start command, and other output around it doesn't matter.
  const fromEnv = !values.describe && process.env.MAXCLOUD_DESCRIBE === "1";
  if (values.describe) process.env.MAXCLOUD_DESCRIBE = "1";
  const found = await load(sourcePath(values.source));

  if (values.describe) {
    process.stdout.write(JSON.stringify(describe(found), null, 2) + "\n");
    return 0;
  }
  if (fromEnv) {
    process.stdout.write(DESCRIBE_TAG + JSON.stringify(describe(found)) + "\n");
    return 0;
  }

  const names = found.map((f) => f.name).join(", ") || "none";
  const target = values.target ?? process.env.FUNCTION_TARGET;
  if (!target) {
    console.error(`maxcloud-functions: which function? Set FUNCTION_TARGET or --target (${names})`);
    return 2;
  }
  const f = found.find((x) => x.name === target || x.exportName === target);
  if (!f) {
    console.error(`maxcloud-functions: no function ${target} (found: ${names})`);
    return 2;
  }

  // Finish cleanly and exit 0, so pods don't end in Error.
  const port = Number(values.port ?? process.env.PORT ?? 8080);
  return run(f, { port, signal: stopSignal() });
}

main().then(
  (code) => process.exit(code),
  (err) => {
    console.error("maxcloud-functions:", err instanceof Error ? (err.stack ?? err.message) : err);
    process.exit(1);
  },
);
