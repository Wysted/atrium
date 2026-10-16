<script setup lang="ts">
import type { NuxtError } from '#app'

/**
 * Custom error page. Nuxt's default one injects an inline <style> and <script>,
 * which the strict CSP blocks. Composed from base components only.
 */
const props = defineProps<{ error: NuxtError }>()

const status = computed(() => props.error.status || props.error.statusCode || 500)
const isNotFound = computed(() => status.value === 404)
const heading = computed(() => (isNotFound.value ? 'Page not found' : 'Something went wrong'))
const description = computed(() =>
  isNotFound.value
    ? 'The page you are looking for does not exist or was moved.'
    : 'An unexpected error occurred. Try again or go back to the start.',
)
// Never show arbitrary error messages to users; dev only.
const devDetail = computed(() => (import.meta.dev ? props.error.message : ''))

useHead({ title: computed(() => `${heading.value} - Portal`) })

function goHome() {
  return clearError({ redirect: '/' })
}
</script>

<template>
  <NuxtLayout>
    <BaseStack :gap="6">
      <BaseStack :gap="2">
        <BaseText tone="muted" size="sm" weight="semibold">
          Error {{ status }}
        </BaseText>
        <BaseHeading :level="1">
          {{ heading }}
        </BaseHeading>
        <BaseText tone="muted">
          {{ description }}
        </BaseText>
        <BaseText v-if="devDetail" tone="danger" size="sm">
          {{ devDetail }}
        </BaseText>
      </BaseStack>
      <BaseCluster>
        <BaseButton variant="primary" @click="goHome">
          Go to home
        </BaseButton>
      </BaseCluster>
    </BaseStack>
  </NuxtLayout>
</template>
