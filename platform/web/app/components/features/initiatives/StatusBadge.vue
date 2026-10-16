<script setup lang="ts">
import type { InitiativeStatus } from '~/types/api'

/** Status of an initiative. The text carries the meaning; the tone is decoration. */
defineProps<{ status: InitiativeStatus }>()
</script>

<template>
  <BaseBadge :tone="status === 'active' ? 'success' : 'neutral'">
    {{ status === 'active' ? 'Active' : 'Inactive' }}
  </BaseBadge>
</template>
