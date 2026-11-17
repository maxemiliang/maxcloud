<script setup lang="ts">
// A Ready condition as mcloud status prints it: the mark, and why when it isn't.
import type { Cond } from '~~/shared/types'

const props = withDefaults(defineProps<{ cond: Cond, observed?: boolean }>(), { observed: true })
const done = computed(() => props.cond.status === 'True' && props.observed)
const why = computed(() => readyText(props.cond, props.observed))
</script>

<template>
  <span>
    <StatusMark :status="done ? 'True' : props.cond.status === 'True' ? 'Unknown' : props.cond.status" />
    <span v-if="why" class="why"> {{ why }}</span>
  </span>
</template>
