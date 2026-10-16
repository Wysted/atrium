<script setup lang="ts">
definePageMeta({ layout: 'admin' })
useHead({ title: 'Health - Administration - Portal' })

const access = useAdminAccess()
</script>

<template>
  <FeaturesHealthOrphanHostsPanel v-if="access.isPlatformAdmin.value" />
  <BaseEmptyState
    v-else
    title="Platform administrators only"
    description="Only platform administrators see orphan hosts."
  >
    <BaseButton to="/admin/initiatives">
      Back to initiatives
    </BaseButton>
  </BaseEmptyState>
</template>
