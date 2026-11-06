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
import { devEntryPath, load, sourcePath } from "./runtime.ts";
import { runFound, stopSignal } from "./start.ts";

const usage = `usage: maxcloud-functions [--target NAME] [--source FILE] [--port N]
       maxcloud-functions --describe [--source FILE]
       maxcloud-functions dev [--entry FILE] [--port N]

Runs one of the app's functions: the HTTP server, a scheduled run, or a queue
worker. NAME defaults to $FUNCTION_TARGET, FILE to package.json's "main", the
port to $PORT or 8080. First it applies the pending SQL migrations in
./migrations to $DATABASE_URL (MAXCLOUD_MIGRATE=0 skips that).

dev runs the whole app on 127.0.0.1: http://<name>.localhost:<port>/ reaches
an HTTP function, GET / lists them, POST /_run/<name> runs a schedule once,
and queue workers run when DATABASE_URL is set. FILE defaults to src/index.ts
if it exists, else package.json's "main".`;

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
  return runFound(found, {
    describe: values.describe ? "json" : fromEnv ? "tagged" : undefined,
    target: values.target,
    port: values.port ? Number(values.port) : undefined,
  });
}

main().then(
  (code) => process.exit(code),
  (err) => {
    console.error("maxcloud-functions:", err instanceof Error ? (err.stack ?? err.message) : err);
    process.exit(1);
  },
);
