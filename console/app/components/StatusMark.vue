<script setup lang="ts">
// mcloud's marks: ✓ ready, ✗ failed, … in progress, - nothing to say.
const props = withDefaults(defineProps<{ status: string, exists?: boolean, word?: string }>(), { exists: true, word: '' })

const kind = computed(() => (!props.exists ? 'none' : props.status === 'True' ? 'ok' : props.status === 'False' ? 'bad' : 'wait'))
const glyph = computed(() => ({ none: '-', ok: '✓', bad: '✗', wait: '…' })[kind.value])
const say = computed(() => props.word || (props.exists ? condWord(props.status) : 'none'))
</script>

<template>
  <span class="mark" :class="kind"><span aria-hidden="true">{{ glyph }}</span><span class="sr">{{ say }}</span></span>
</template>
