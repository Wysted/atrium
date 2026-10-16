<script setup lang="ts">
/**
 * `to` is an internal route (NuxtLink); `href` is an external URL (plain <a>).
 * `href` must be a trusted URL (literal or built from known constants), never a user-controlled value:
 * `href` is not sanitized, so `javascript:` and `data:` URLs would not be blocked. Hrefs built from data must come
 * from a validating helper (`hostUrl()` in app/utils/hostname.ts, used by FeaturesHomeAppCard).
 * External links with target="_blank" always get rel="noopener noreferrer".
 */
const props = defineProps<{
  to?: string
  href?: string
  target?: '_self' | '_blank'
}>()

const rel = computed(() => (props.target === '_blank' ? 'noopener noreferrer' : undefined))
const linkClass
  = 'rounded-sm text-primary underline underline-offset-2 hover:text-primary-hover focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus'
</script>

<template>
  <NuxtLink v-if="to" :to="to" :target="target" :rel="rel" :class="linkClass">
    <slot />
  </NuxtLink>
  <a v-else :href="href" :target="target" :rel="rel" :class="linkClass">
    <slot />
  </a>
</template>
