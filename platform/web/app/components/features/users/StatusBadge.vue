<script setup lang="ts">
import type { UserStatus } from '~/types/api'

/** Account status of a user. The text carries the meaning; the tone is decoration. */
defineProps<{ status: UserStatus }>()
</script>

<template>
  <BaseBadge :tone="status === 'active' ? 'success' : 'warning'">
    {{ status === 'active' ? 'Active' : 'Disabled' }}
  </BaseBadge>
</template>
