<script setup lang="ts">
import type { AppStatus } from '~/types/api'

/** Status of an app. The text carries the meaning; the tone is decoration. */
defineProps<{ status: AppStatus }>()
</script>

<template>
  <BaseBadge :tone="status === 'active' ? 'success' : 'neutral'">
    {{ status === 'active' ? 'Active' : 'Disabled' }}
  </BaseBadge>
</template>
