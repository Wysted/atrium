<script setup lang="ts">
definePageMeta({ layout: 'admin' })

const route = useRoute()
const appId = String(route.params.appId)
const { initiative } = useInitiativeContext()
useHead({ title: computed(() => `App health - ${initiativeLabel(initiative.value)} - Administration - Portal`) })
</script>

<template>
  <FeaturesHealthAppPanel :app-id="appId" :initiative-id="initiative.id" />
</template>
