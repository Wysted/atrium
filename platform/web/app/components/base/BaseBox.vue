<script setup lang="ts">
import type { Space } from '~/types/ui'

withDefaults(defineProps<{
  as?: string
  padding?: Space
  surface?: 'none' | 'base' | 'raised'
  bordered?: boolean
  rounded?: 'none' | 'sm' | 'md' | 'lg'
  shadow?: 'none' | 'sm' | 'md'
}>(), {
  as: 'div',
  padding: 0,
  surface: 'none',
  bordered: false,
  rounded: 'none',
  shadow: 'none',
})

const paddings: Record<Space, string> = {
  0: 'p-0',
  1: 'p-1',
  2: 'p-2',
  3: 'p-3',
  4: 'p-4',
  6: 'p-6',
  8: 'p-8',
  12: 'p-12',
}
const surfaces = { none: '', base: 'bg-surface', raised: 'bg-surface-raised' }
const radii = { none: '', sm: 'rounded-sm', md: 'rounded-md', lg: 'rounded-lg' }
const shadows = { none: '', sm: 'shadow-sm', md: 'shadow-md' }
</script>

<template>
  <component
    :is="as"
    :class="[
      paddings[padding],
      surfaces[surface],
      radii[rounded],
      shadows[shadow],
      bordered && 'border border-border',
    ]"
  >
    <slot />
  </component>
</template>
