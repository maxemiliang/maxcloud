#!/usr/bin/env node
// maxcloud-functions: the container's entrypoint.
//
//   maxcloud-functions                 run $FUNCTION_TARGET
//   maxcloud-functions --target hello  run that function
//   maxcloud-functions --describe      print the app's functions as JSON
//   MAXCLOUD_DESCRIBE=1 maxcloud-functions   the same, as one tagged line

import { parseArgs } from "node:util";
import { DESCRIBE_TAG, describe, load, run, sourcePath } from "./runtime.ts";

const usage = `usage: maxcloud-functions [--target NAME] [--source FILE] [--port N]
       maxcloud-functions --describe [--source FILE]

Runs one of the app's functions: the HTTP server, a scheduled run, or a queue
worker. NAME defaults to $FUNCTION_TARGET, FILE to package.json's "main", the
port to $PORT or 8080.`;

async function main(): Promise<number> {
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

  // SIGTERM is how Kubernetes stops a pod; finish cleanly and exit 0.
  const stop = new AbortController();
  for (const sig of ["SIGTERM", "SIGINT"] as const) {
    process.once(sig, () => stop.abort(new Error(`${sig} received`)));
  }
  const port = Number(values.port ?? process.env.PORT ?? 8080);
  return run(f, { port, signal: stop.signal });
}

main().then(
  (code) => process.exit(code),
  (err) => {
    console.error("maxcloud-functions:", err instanceof Error ? (err.stack ?? err.message) : err);
    process.exit(1);
  },
);
