<script setup lang="ts">
import type { IconName } from '~/types/ui'

/**
 * Curated inline SVG set (24x24, stroke = currentColor); no dependency, no <style>.
 * Without `label` the icon is decorative (aria-hidden). Icon-only controls must pass `label`.
 */
const props = defineProps<{
  name: IconName
  label?: string
  size?: 'sm' | 'md' | 'lg'
}>()

const paths: Record<IconName, string[]> = {
  'check': ['M20 6 9 17l-5-5'],
  'loader': ['M21 12a9 9 0 1 1-6.219-8.56'],
  'x': ['M18 6 6 18', 'm6 6 12 12'],
  'plus': ['M5 12h14', 'M12 5v14'],
  'minus': ['M5 12h14'],
  'chevron-down': ['m6 9 6 6 6-6'],
  'chevron-right': ['m9 18 6-6-6-6'],
  'external-link': ['M15 3h6v6', 'M10 14 21 3', 'M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6'],
  'alert-triangle': ['m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3', 'M12 9v4', 'M12 17h.01'],
  'info': ['M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20z', 'M12 16v-4', 'M12 8h.01'],
  'search': ['M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16z', 'm21 21-4.3-4.3'],
}

// Dev-only guards. Without `label` the icon is decorative (aria-hidden), so a role/aria-label passed
// as a fallthrough attribute would contradict it: the accessible name must come from `label`.
if (import.meta.dev) {
  const attrs = useAttrs()
  if (!(props.name in paths)) console.warn(`[BaseIcon] unknown icon name "${props.name}"`)
  if (props.label !== undefined && props.label.trim() === '') {
    console.warn(`[BaseIcon] "${props.name}" has an empty label; omit it to make the icon decorative`)
  }
  if (!props.label && (attrs.role === 'img' || attrs['aria-label'])) {
    console.warn(`[BaseIcon] "${props.name}" looks meaningful (role/aria-label attr) but has no \`label\` prop; it will be aria-hidden`)
  }
}
const sizes = { sm: 'size-4', md: 'size-5', lg: 'size-6' }
const size = computed(() => sizes[props.size ?? 'md'])
</script>

<template>
  <svg
    xmlns="http://www.w3.org/2000/svg"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    :class="['inline-block shrink-0', size]"
    :role="label ? 'img' : undefined"
    :aria-label="label"
    :aria-hidden="label ? undefined : 'true'"
    focusable="false"
  >
    <path v-for="d in paths[name]" :key="d" :d="d" />
  </svg>
</template>
