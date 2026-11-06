// A module that is its own entrypoint, like a bundled Nitro server.
import { isEntrypoint, onRequest, onShutdown, start } from "../index.ts";

if (process.env.MAXCLOUD_DESCRIBE !== "1") console.log("app initialised");
onShutdown(() => console.log("shutdown hook ran"));

export const web = onRequest({ node: (req, res) => res.end(`web ${req.url}`) });

if (await isEntrypoint(import.meta.url)) start({ web });
