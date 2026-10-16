<script setup lang="ts">
import type { Align, Justify, Space } from '~/types/ui'

withDefaults(defineProps<{
  as?: string
  gap?: Space
  align?: Align
  justify?: Justify
  wrap?: boolean
}>(), {
  as: 'div',
  gap: 2,
  align: 'center',
  justify: 'start',
  wrap: true,
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
const justifies: Record<Justify, string> = {
  start: 'justify-start',
  center: 'justify-center',
  end: 'justify-end',
  between: 'justify-between',
}
</script>

<template>
  <component
    :is="as"
    :class="['flex', wrap ? 'flex-wrap' : 'flex-nowrap', gaps[gap], aligns[align], justifies[justify]]"
  >
    <slot />
  </component>
</template>
