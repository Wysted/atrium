<script setup lang="ts">
import type { Space } from '~/types/ui'

/** Responsive auto-fill grid; `minColumn` picks the minimum column width from the container tokens. */
withDefaults(defineProps<{
  as?: string
  gap?: Space
  minColumn?: 'sm' | 'md' | 'lg'
}>(), {
  as: 'div',
  gap: 4,
  minColumn: 'md',
})

const gaps: Record<Space, string> = {
  0: 'gap-0',
  1: 'gap-1',
  2: 'gap-2',
  3: 'gap-3',
  4: 'gap-4',
  6: 'gap-6',
  8: 'gap-8',
  12: 'gap-12',
}
const columns = {
  sm: 'grid-cols-[repeat(auto-fill,minmax(min(var(--container-grid-sm),100%),1fr))]',
  md: 'grid-cols-[repeat(auto-fill,minmax(min(var(--container-grid-md),100%),1fr))]',
  lg: 'grid-cols-[repeat(auto-fill,minmax(min(var(--container-grid-lg),100%),1fr))]',
}
</script>

<template>
  <component :is="as" :class="['grid', gaps[gap], columns[minColumn]]">
    <slot />
  </component>
</template>
