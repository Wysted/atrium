<script setup lang="ts">
definePageMeta({ layout: 'admin' })
useHead({ title: 'Group members - Administration - Portal' })

const access = useAdminAccess()
const route = useRoute()
const groupId = computed(() => String(route.params.id))
</script>

<template>
  <FeaturesGroupsMembersPanel v-if="access.isPlatformAdmin.value" :key="groupId" :group-id="groupId" />
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
