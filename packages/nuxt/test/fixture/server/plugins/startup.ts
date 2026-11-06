// Like lankkupaja's migration plugin: runs at startup, closes on shutdown.
export default defineNitroPlugin((nitroApp) => {
  globalThis.__startedAt = Date.now();
  console.log("[fixture] plugin started");
  nitroApp.hooks.hook("close", () => console.log("[fixture] plugin closed"));
});
