<script setup lang="ts">
import type { HeadingLevel, TextSize } from '~/types/ui'

/** `level` decides the semantic tag (h1..h6); `size` decides how it looks. Defaults to the level's usual size. */
const props = defineProps<{
  level: HeadingLevel
  size?: TextSize
}>()

const defaultSizes: Record<HeadingLevel, TextSize> = {
  1: '3xl',
  2: '2xl',
  3: 'xl',
  4: 'lg',
  5: 'base',
  6: 'sm',
}
const sizes: Record<TextSize, string> = {
  'xs': 'text-xs',
  'sm': 'text-sm',
  'base': 'text-base',
  'lg': 'text-lg',
  'xl': 'text-xl',
  '2xl': 'text-2xl',
  '3xl': 'text-3xl',
  '4xl': 'text-4xl',
}

const tag = computed(() => `h${props.level}`)
const sizeClass = computed(() => sizes[props.size ?? defaultSizes[props.level]])
</script>

<template>
  <component :is="tag" :class="[sizeClass, 'font-semibold text-text']">
    <slot />
  </component>
</template>
