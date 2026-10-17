<script setup lang="ts">
definePageMeta({ layout: 'admin' })
useHead({ title: 'Areas - Administration - Portal' })

const access = useAdminAccess()
</script>

<template>
  <FeaturesAreasPanel v-if="access.isPlatformAdmin.value" />
  <BaseEmptyState
    v-else
    title="Platform administrators only"
    description="Only platform administrators manage areas and their people."
  >
    <BaseButton to="/admin/initiatives">
      Back to initiatives
    </BaseButton>
  </BaseEmptyState>
</template>
