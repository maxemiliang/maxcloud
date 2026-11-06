// The server entries the module gives Nitro, as source code (virtual
// modules). Paths are absolute, so the bundle doesn't depend on how the app
// resolves packages.

import type { HttpOptions } from "@max-cloud/functions";

/** The app as a maxcloud function: its name and onRequest's options. */
export interface FunctionOptions extends HttpOptions {
  /** The function's (and the Knative service's) name. Default "web". */
  name?: string;
  /**
   * Nitro's scheduledTasks as maxcloud scheduled functions (CronJobs), one
   * per task: `task-<name>`. Default true. Off: they don't run at all
   * (Nitro's own runner needs a long-running server; a function scales to
   * zero).
   */
  tasks?: boolean;
}

export interface EntryPaths {
  /** The virtual module that sets NUXT_* from maxcloud's environment. */
  env: string;
  /** @max-cloud/functions' entry. */
  functions: string;
}

/**
 * The env module: sets the NUXT_* variables before anything reads them.
 * Unless `migrations`, it also turns off @max-cloud/functions' migrations
 * at startup: a Nuxt app migrates in its own Nitro plugin (drizzle's
 * migrator, say), often from a migrations/ folder the SDK would read too.
 */
export function envModule(envFile: string, map: Record<string, string>, migrations = false): string {
  return `import { applyMaxcloudEnv } from ${JSON.stringify(envFile)};
applyMaxcloudEnv(${JSON.stringify(map)});
${migrations ? "" : 'process.env.MAXCLOUD_MIGRATE ??= "0";\n'}`;
}

/**
 * An existing server entry (Nitro's node-server), with maxcloud's
 * environment mapped first. ES modules run their imports in order, so the
 * env module runs before the entry's imports read the runtimeConfig.
 */
export function wrapEntry(paths: Pick<EntryPaths, "env">, original: string): string {
  return `import ${JSON.stringify(paths.env)};
export * from ${JSON.stringify(original)};
export { default } from ${JSON.stringify(original)};
`;
}

/** A JavaScript identifier for a task's export: db:cleanup -> taskDbCleanup (deployed as task-db-cleanup). */
export function taskExport(task: string): string {
  const words = task.split(/[^A-Za-z0-9]+/).filter(Boolean);
  return "task" + words.map((w) => w[0]!.toUpperCase() + w.slice(1)).join("");
}

/** One onSchedule per task: its cron schedules (Nitro allows several), its export name. */
export function scheduledTasks(scheduled: Record<string, string | string[]> | undefined): { task: string; exportName: string; schedule: string }[] {
  const out: { task: string; exportName: string; schedule: string }[] = [];
  const used = new Set<string>();
  for (const [cron, tasks] of Object.entries(scheduled ?? {})) {
    for (const task of [tasks].flat()) {
      let exportName = taskExport(task);
      for (let i = 2; used.has(exportName); i++) exportName = taskExport(task) + i;
      used.add(exportName);
      out.push({ task, exportName, schedule: cron });
    }
  }
  return out;
}

/**
 * The function entry: the Nitro app behind onRequest, as a Node listener
 * (streaming, the request's own Host), and Nitro's scheduled tasks as
 * onSchedule functions.
 *
 * The Nitro app (and so its plugins: migrations, storage mounts) starts
 * when the module loads, unless MAXCLOUD_DESCRIBE=1: mcloud only asks the
 * image what it is then, without a database or secrets. Run with node, the
 * entry is the container's command itself (start); loaded by
 * maxcloud-functions (package.json main), it's a module like any other.
 */
export function functionEntry(paths: EntryPaths, fn: FunctionOptions, scheduled?: Record<string, string | string[]>): string {
  const { name = "web", tasks = true, ...http } = fn;
  const exportName = name.replace(/-([a-z0-9])/g, (_, c: string) => c.toUpperCase());
  const taskFns = tasks ? scheduledTasks(scheduled) : [];
  // Tasks run in the same image: they need its container settings (a user
  // for an image without USER, the volumes a read-only root needs).
  const container = Object.fromEntries(
    (["memory", "memoryLimit", "cpu", "user", "readOnlyRootFilesystem", "volumes"] as const)
      .filter((k) => http[k] !== undefined)
      .map((k) => [k, http[k]]),
  );
  const lines = [
    `import ${JSON.stringify(paths.env)};`,
    `import "#nitro-internal-pollyfills";`,
    `import { isEntrypoint, onRequest, onSchedule, onShutdown, start } from ${JSON.stringify(paths.functions)};`,
    ``,
    `let nitroApp, listener;`,
    `// Nitro's runtime (and h3, which Nitro's chunk holds) is imported here,`,
    `// not at the top: importing it creates the app and runs its plugins, and`,
    `// its runtimeConfig must see the environment the env module just set.`,
    `async function nitro() {`,
    `  if (!nitroApp) {`,
    `    const runtime = await import("nitropack/runtime");`,
    `    const internal = await import("nitropack/runtime/internal");`,
    `    const { toNodeListener } = await import("h3");`,
    `    internal.trapUnhandledNodeErrors();`,
    `    nitroApp = runtime.useNitroApp();`,
    `    listener = toNodeListener(nitroApp.h3App);`,
    `    // Nitro's close hook (plugins close their pools there) once requests are done.`,
    `    onShutdown(() => nitroApp.hooks.callHook("close"));`,
    `  }`,
    `  return nitroApp;`,
    `}`,
    `if (process.env.MAXCLOUD_DESCRIBE !== "1") await nitro();`,
    ``,
    `export const ${exportName} = onRequest(${JSON.stringify(http)}, {`,
    `  node: (req, res) => (listener ? listener(req, res) : nitro().then(() => listener(req, res))),`,
    `});`,
  ];
  const exportsList = [exportName];
  for (const t of taskFns) {
    lines.push(
      `export const ${t.exportName} = onSchedule(${JSON.stringify({ schedule: t.schedule, ...container })}, async () => {`,
      `  await nitro();`,
      `  const { runTask } = await import("nitropack/runtime");`,
      `  const { result } = await runTask(${JSON.stringify(t.task)}, { payload: { scheduledTime: Date.now() }, context: {} });`,
      `  console.log(${JSON.stringify(`task ${t.task}:`)}, JSON.stringify(result ?? null));`,
      `});`,
    );
    exportsList.push(t.exportName);
  }
  lines.push(
    ``,
    `// node .output/server/index.mjs serves the app, as Nitro's own server would.`,
    `if (await isEntrypoint(import.meta.url)) start({ ${exportsList.join(", ")} }, { target: process.env.FUNCTION_TARGET || ${JSON.stringify(name)} });`,
    `export default {};`,
    ``,
  );
  return lines.join("\n");
}
