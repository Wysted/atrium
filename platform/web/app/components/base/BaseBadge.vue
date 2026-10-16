<script setup lang="ts">
type BadgeTone = 'neutral' | 'primary' | 'success' | 'warning' | 'danger'

/** A small label for a status or a role. Tone is decoration: the text must carry the meaning. */
withDefaults(defineProps<{
  tone?: BadgeTone
}>(), {
  tone: 'neutral',
})

const tones: Record<BadgeTone, string> = {
  neutral: 'border-border text-text-muted',
  primary: 'border-primary text-primary',
  success: 'border-success text-success',
  warning: 'border-warning text-warning',
  danger: 'border-danger text-danger',
}
</script>

<template>
  <span
    class="inline-flex items-center rounded-sm border bg-surface px-2 py-1 text-xs font-medium"
    :class="tones[tone]"
  >
    <slot />
  </span>
</template>
