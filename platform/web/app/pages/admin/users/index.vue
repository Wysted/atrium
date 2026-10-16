<script setup lang="ts">
definePageMeta({ layout: 'admin' })
useHead({ title: 'Users - Administration - Portal' })

const access = useAdminAccess()
</script>

<template>
  <FeaturesUsersPanel v-if="access.isPlatformAdmin.value" />
  <BaseEmptyState
    v-else
    title="Platform administrators only"
    description="Only platform administrators manage users."
  >
    <BaseButton to="/admin/initiatives">
      Back to initiatives
    </BaseButton>
  </BaseEmptyState>
</template>
