<script setup lang="ts">
definePageMeta({ layout: 'admin' })
useHead({ title: 'Robot - Administration - Portal' })

const access = useAdminAccess()
const route = useRoute()
const robotId = computed(() => String(route.params.id))
</script>

<template>
  <FeaturesRobotsDetailPanel v-if="access.isPlatformAdmin.value" :key="robotId" :robot-id="robotId" />
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
