<script setup lang="ts">
definePageMeta({ layout: 'admin' })

const access = useAdminAccess()
const { initiative } = useInitiativeContext()
useHead({ title: computed(() => `Apps - ${initiativeLabel(initiative.value)} - Administration - Portal`) })
</script>

<template>
  <FeaturesAppsPanel
    :initiative-id="initiative.id"
    :can-edit="access.isPlatformAdmin.value"
  />
</template>
