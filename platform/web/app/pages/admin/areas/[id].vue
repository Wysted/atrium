<script setup lang="ts">
definePageMeta({ layout: 'admin' })
useHead({ title: 'Area - Administration - Portal' })

const access = useAdminAccess()
const route = useRoute()
const areaId = computed(() => String(route.params.id))
</script>

<template>
  <FeaturesAreasDetailPanel v-if="access.isPlatformAdmin.value" :key="areaId" :area-id="areaId" />
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
