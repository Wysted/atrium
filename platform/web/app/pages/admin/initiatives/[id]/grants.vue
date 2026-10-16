<script setup lang="ts">
definePageMeta({ layout: 'admin' })

const access = useAdminAccess()
const { initiative } = useInitiativeContext()
useHead({ title: computed(() => `Grants - ${initiativeLabel(initiative.value)} - Administration - Portal`) })
</script>

<template>
  <FeaturesGrantsPanel
    :initiative-id="initiative.id"
    :can-edit="access.managesInitiative(initiative.id)"
  />
</template>
