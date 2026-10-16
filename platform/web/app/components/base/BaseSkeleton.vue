<script setup lang="ts">
/**
 * Decorative placeholder block while content loads (hidden from assistive tech). The loading region itself
 * must announce the state (aria-busy plus visually hidden text).
 */
withDefaults(defineProps<{
  height?: 'sm' | 'md' | 'lg'
  width?: 'half' | 'full'
}>(), {
  height: 'md',
  width: 'full',
})

const heights = { sm: 'h-3', md: 'h-5', lg: 'h-8' }
const widths = { half: 'w-1/2', full: 'w-full' }
</script>

<template>
  <div
    aria-hidden="true"
    class="rounded-sm bg-border motion-safe:animate-pulse"
    :class="[heights[height], widths[width]]"
  />
</template>
