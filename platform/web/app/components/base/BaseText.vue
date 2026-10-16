<script setup lang="ts">
import type { TextSize, TextTone, TextWeight } from '~/types/ui'

withDefaults(defineProps<{
  as?: string
  size?: TextSize
  tone?: TextTone
  weight?: TextWeight
}>(), {
  as: 'p',
  size: 'base',
  tone: 'default',
  weight: 'normal',
})

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
const tones: Record<TextTone, string> = {
  default: 'text-text',
  muted: 'text-text-muted',
  primary: 'text-primary',
  danger: 'text-danger',
  warning: 'text-warning',
  success: 'text-success',
}
const weights: Record<TextWeight, string> = {
  normal: 'font-normal',
  medium: 'font-medium',
  semibold: 'font-semibold',
  bold: 'font-bold',
}
</script>

<template>
  <component :is="as" :class="[sizes[size], tones[tone], weights[weight]]">
    <slot />
  </component>
</template>
