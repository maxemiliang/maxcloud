// @max-cloud/nuxt: a Nuxt app on maxcloud.
//
//   - runtimeConfig from what the platform gives the app (DATABASE_URL, the
//     app's Object Storage credentials and bucket names, MAXCLOUD_APP), as
//     NUXT_* variables set before Nitro reads them: runtimeConfig.maxcloud,
//     and any of the app's own keys mapped with `runtimeConfig`;
//   - with `function`, the server as a maxcloud HTTP function (and Nitro's
//     scheduled tasks as scheduled functions), so the deploy workflow
//     generates its Knative service (maxcloud.yaml functions: true).

import { addTemplate, createResolver, defineNuxtModule, useLogger } from "@nuxt/kit";
import { fileURLToPath } from "node:url";
import { functionEntry, envModule, wrapEntry, type FunctionOptions } from "./entry.ts";
import { applyEnv, byEnvName, maxcloudMap, type RuntimeConfigMap } from "./env.ts";
import { readProject } from "./project.ts";

export type { FunctionOptions } from "./entry.ts";
export type { RuntimeConfigMap, Source } from "./env.ts";

export interface ModuleOptions {
  /**
   * The app's own runtimeConfig keys and where their values come from:
   * `{ databaseUrl: "database", "storage.s3": "s3:uploads" }`. See Source.
   */
  runtimeConfig?: RuntimeConfigMap;
  /** Buckets for runtimeConfig.maxcloud.buckets. Default: maxcloud.yaml's. */
  buckets?: string[];
  /** Build the server as a maxcloud function (onRequest options, and its name). */
  function?: FunctionOptions | boolean;
}

interface NitroOptions {
  preset: string;
  entry: string;
  virtual: Record<string, string | (() => string)>;
  scheduledTasks?: Record<string, string | string[]>;
}

const ENV_MODULE = "#maxcloud/env";
const ENTRY = "#maxcloud/entry";

export default defineNuxtModule<ModuleOptions>({
  meta: { name: "@max-cloud/nuxt", configKey: "maxcloud", compatibility: { nuxt: ">=4.0.0" } },
  defaults: {},
  setup(options, nuxt) {
    const logger = useLogger("maxcloud");
    const { resolve } = createResolver(import.meta.url);
    const project = readProject(nuxt.options.rootDir);
    const buckets = options.buckets ?? project.buckets;

    // runtimeConfig.maxcloud, filled from the environment (server only).
    const rc = nuxt.options.runtimeConfig as Record<string, unknown>;
    rc.maxcloud = {
      app: project.app ?? "",
      databaseUrl: "",
      s3: { endpoint: "", region: "", accessKeyId: "", secretAccessKey: "" },
      buckets: Object.fromEntries(buckets.map((b) => [b, ""])),
      ...(rc.maxcloud as object | undefined),
    };
    const map: RuntimeConfigMap = { ...maxcloudMap(buckets), ...options.runtimeConfig };

    if (nuxt.options.dev) {
      // nuxt dev: the Nitro dev server inherits this process's environment.
      applyEnv(map);
      return;
    }

    const envFile = resolve("./env-runtime.js");
    const functionsEntry = fileURLToPath(import.meta.resolve("@max-cloud/functions"));
    const fn: FunctionOptions | undefined = options.function === true ? {} : options.function || undefined;

    nuxt.hook("nitro:config", (config) => {
      config.virtual ??= {};
      config.virtual[ENV_MODULE] = envModule(envFile, byEnvName(map));
    });

    nuxt.hook("nitro:init", (nitro) => {
      // Nitro's resolved options (typed loosely by @nuxt/schema).
      const o = nitro.options as unknown as NitroOptions;
      const preset = String(o.preset);
      if (fn) {
        if (!preset.startsWith("node")) {
          logger.warn(`function: the Nitro preset is ${preset}; the function entry needs Node.js (preset node-server)`);
        }
        o.virtual[ENTRY] = functionEntry({ env: ENV_MODULE, functions: functionsEntry }, fn, o.scheduledTasks);
        o.entry = ENTRY;
        logger.info(`building the server as maxcloud function ${fn.name ?? "web"} (.output/server/index.mjs)`);
      } else if (preset.startsWith("node")) {
        o.virtual[ENTRY] = wrapEntry({ env: ENV_MODULE }, o.entry);
        o.entry = ENTRY;
      } else {
        logger.warn(`the Nitro preset ${preset} isn't Node.js: set the NUXT_* variables yourself`);
      }
    });

    addTemplate({ filename: "maxcloud.mjs", getContents: () => `export default ${JSON.stringify({ buckets, function: fn?.name ?? null })}` });
  },
});
