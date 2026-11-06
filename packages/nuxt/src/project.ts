// The app's maxcloud.yaml, for its name and buckets at build time.

import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { parse } from "yaml";

export interface Project {
  app?: string;
  buckets: string[];
  file?: string;
}

/** maxcloud.yaml in dir or a parent (mcloud looks the same way); empty if there's none. */
export function readProject(dir: string): Project {
  for (let d = dir; ; d = dirname(d)) {
    const file = join(d, "maxcloud.yaml");
    if (existsSync(file)) {
      const doc = (parse(readFileSync(file, "utf8")) ?? {}) as { app?: unknown; buckets?: unknown };
      const buckets = Array.isArray(doc.buckets) ? doc.buckets.map((b) => String(b).split(":")[0]!.trim()).filter(Boolean) : [];
      return { app: typeof doc.app === "string" ? doc.app : undefined, buckets, file };
    }
    if (dirname(d) === d) return { buckets: [] };
  }
}
