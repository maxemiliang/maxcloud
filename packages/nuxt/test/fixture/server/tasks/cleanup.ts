export default defineTask({
  meta: { name: "cleanup", description: "A nightly job" },
  run({ payload }) {
    console.log("[fixture] cleanup ran", typeof payload.scheduledTime);
    return { result: "cleaned" };
  },
});
