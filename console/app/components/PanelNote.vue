<script setup lang="ts">
// What a panel says when it has nothing to show: still loading, not for
// this login, not offered by the platform, or down.
defineProps<{ pending?: boolean, message?: string }>()
</script>

<template>
  <p class="note" :class="{ busy: pending }">
    {{ pending ? 'Asking the platform…' : message }}
  </p>
</template>
