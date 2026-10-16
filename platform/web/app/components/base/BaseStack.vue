<script setup lang="ts">
import type { Align, Space } from '~/types/ui'

withDefaults(defineProps<{
  as?: string
  gap?: Space
  align?: Align
}>(), {
  as: 'div',
  gap: 4,
  align: 'stretch',
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
const aligns: Record<Align, string> = {
  start: 'items-start',
  center: 'items-center',
  end: 'items-end',
  stretch: 'items-stretch',
  baseline: 'items-baseline',
}
</script>

<template>
  <component :is="as" :class="['flex flex-col', gaps[gap], aligns[align]]">
    <slot />
  </component>
</template>
