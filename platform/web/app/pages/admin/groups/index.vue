<script setup lang="ts">
definePageMeta({ layout: 'admin' })
useHead({ title: 'Groups - Administration - Portal' })

const access = useAdminAccess()
</script>

<template>
  <FeaturesGroupsPanel v-if="access.isPlatformAdmin.value" />
  <BaseEmptyState
    v-else
    title="Platform administrators only"
    description="Only platform administrators manage groups and their members."
  >
    <BaseButton to="/admin/initiatives">
      Back to initiatives
    </BaseButton>
  </BaseEmptyState>
</template>
