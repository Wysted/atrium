<script setup lang="ts">
// Client-side redirect before the page renders (no flash): the section lands on its first screen.
definePageMeta({
  layout: 'admin',
  middleware: () => navigateTo('/admin/initiatives', { replace: true }),
})
</script>

<template>
  <BaseText tone="muted">
    Redirecting to initiatives.
  </BaseText>
</template>
