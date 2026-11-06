export default defineNuxtConfig({
  modules: ["@max-cloud/nuxt"],
  compatibilityDate: "2026-06-30",
  devtools: { enabled: false },
  telemetry: false,

  // The app's own keys, as lankkupaja has them
  runtimeConfig: {
    databaseUrl: "",
    storage: { driver: "fs", s3: { endpoint: "", region: "", bucket: "", accessKeyId: "", secretAccessKey: "" } },
  },

  maxcloud: {
    runtimeConfig: { databaseUrl: "database", "storage.s3": "s3:uploads" },
    function: {
      name: "web",
      public: true,
      minInstances: 1,
      maxInstances: 2,
      memory: "384Mi",
      memoryLimit: "1536Mi",
      user: 1000,
      readOnlyRootFilesystem: true,
      volumes: [{ path: "/scratch", sizeLimit: "1Gi" }, { path: "/dev/shm", sizeLimit: "512Mi", memory: true }],
      healthPath: "/api/health",
    },
  },

  nitro: {
    experimental: { tasks: true },
    scheduledTasks: { "0 3 * * *": ["cleanup"] },
  },

  routeRules: { "/spa": { ssr: false } },
});
