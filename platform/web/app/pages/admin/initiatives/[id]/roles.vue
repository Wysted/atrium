<script setup lang="ts">
definePageMeta({ layout: 'admin' })

const access = useAdminAccess()
const { initiative } = useInitiativeContext()
useHead({ title: computed(() => `Roles - ${initiativeLabel(initiative.value)} - Administration - Portal`) })
</script>

<template>
  <FeaturesRolesPanel
    :initiative-id="initiative.id"
    :can-edit="access.isPlatformAdmin.value"
  />
</template>
