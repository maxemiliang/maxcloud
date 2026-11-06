<script setup lang="ts">
const { data } = await useFetch("/api/host");
const count = ref(0);
</script>
<template>
  <main>
    <h1>Fixture on maxcloud</h1>
    <p id="host">{{ data?.host }}</p>
    <button @click="count++">clicked {{ count }}</button>
  </main>
</template>
