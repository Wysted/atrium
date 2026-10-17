<script setup lang="ts">
definePageMeta({ layout: 'admin' })
useHead({ title: 'Robots - Administration - Portal' })

const access = useAdminAccess()
</script>

<template>
  <FeaturesRobotsPanel v-if="access.isPlatformAdmin.value" />
  <BaseEmptyState
    v-else
    title="Platform administrators only"
    description="Only platform administrators manage robots and their tokens."
  >
    <BaseButton to="/admin/initiatives">
      Back to initiatives
    </BaseButton>
  </BaseEmptyState>
</template>
