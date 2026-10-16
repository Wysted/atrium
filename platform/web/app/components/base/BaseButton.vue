<script setup lang="ts">
import type { ButtonSize, ButtonVariant } from '~/types/ui'

/**
 * Renders NuxtLink when `to` is set, <a> when `href` is set, otherwise <button>.
 * Disabled/loading links render as <a> without href and with aria-disabled, so they are not actionable.
 * `href` must be a trusted URL (literal or built from known constants), never a user-controlled value:
 * `href` is not sanitized, so `javascript:` and `data:` URLs would not be blocked. Hrefs built from data must come
 * from a validating helper (`hostUrl()` in app/utils/hostname.ts).
 *
 * `click` is declared as an emit, so a parent `@click` does not fall through to the root element: it fires
 * only when the button is active. While `loading`, the <button> stays natively enabled (keeps focus) and
 * uses aria-disabled; the click handler blocks the default action and propagation. `disabled` uses the
 * native attribute on buttons.
 */
const props = withDefaults(defineProps<{
  to?: string
  href?: string
  target?: '_self' | '_blank'
  type?: 'button' | 'submit' | 'reset'
  variant?: ButtonVariant
  size?: ButtonSize
  disabled?: boolean
  loading?: boolean
}>(), {
  to: undefined,
  href: undefined,
  target: undefined,
  type: 'button',
  variant: 'primary',
  size: 'md',
})

const emit = defineEmits<{ click: [event: MouseEvent] }>()

const variants: Record<ButtonVariant, string> = {
  primary: 'bg-primary text-primary-contrast border-transparent',
  secondary: 'bg-surface text-text border-border',
  ghost: 'bg-transparent text-text border-transparent',
  danger: 'bg-danger text-danger-contrast border-transparent',
}
// Applied only when the button is actionable (not disabled or loading).
const hoverVariants: Record<ButtonVariant, string> = {
  primary: 'hover:bg-primary-hover',
  secondary: 'hover:bg-surface-raised',
  ghost: 'hover:bg-surface-raised',
  danger: 'hover:bg-danger-hover',
}
const sizes: Record<ButtonSize, string> = {
  sm: 'px-3 py-1 text-sm',
  md: 'px-4 py-2 text-base',
  lg: 'px-6 py-3 text-lg',
}

const inactive = computed(() => props.disabled || props.loading)
function onClick(event: MouseEvent) {
  if (inactive.value) {
    event.preventDefault()
    event.stopImmediatePropagation()
    return
  }
  emit('click', event)
}
const rel = computed(() => (props.target === '_blank' ? 'noopener noreferrer' : undefined))
const classes = computed(() => [
  'inline-flex items-center justify-center gap-2 rounded-md border font-medium no-underline',
  'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus',
  variants[props.variant],
  sizes[props.size],
  inactive.value ? 'cursor-not-allowed opacity-60' : ['cursor-pointer', hoverVariants[props.variant]],
])
</script>

<template>
  <NuxtLink
    v-if="to && !inactive"
    :to="to"
    :target="target"
    :rel="rel"
    :class="classes"
    @click="onClick"
  >
    <slot />
  </NuxtLink>
  <a
    v-else-if="href || to"
    :href="inactive ? undefined : href"
    :target="target"
    :rel="rel"
    :class="classes"
    :role="inactive ? 'link' : undefined"
    :aria-disabled="inactive || undefined"
    :aria-busy="loading || undefined"
    :tabindex="inactive ? -1 : undefined"
    @click="onClick"
  >
    <BaseIcon v-if="loading" name="loader" class="motion-safe:animate-spin" />
    <slot />
  </a>
  <button
    v-else
    :type="type"
    :class="classes"
    :disabled="disabled"
    :aria-disabled="loading || undefined"
    :aria-busy="loading || undefined"
    @click="onClick"
  >
    <BaseIcon v-if="loading" name="loader" class="motion-safe:animate-spin" />
    <slot />
  </button>
</template>
