<script setup lang="ts">
definePageMeta({ layout: 'admin' })

const access = useAdminAccess()
const { initiative, set } = useInitiativeContext()
</script>

<template>
  <BaseStack :gap="6">
    <BaseText v-if="!access.isPlatformAdmin.value" size="sm" tone="muted">
      You can see this initiative; only platform administrators rename or deactivate it.
    </BaseText>
    <BaseCard as="section" aria-labelledby="initiative-overview" :padding="6">
      <BaseStack :gap="4">
        <BaseHeading id="initiative-overview" :level="2" size="lg">
          Overview
        </BaseHeading>
        <FeaturesInitiativesNameForm
          :initiative="initiative"
          :editable="access.isPlatformAdmin.value"
          @saved="set($event)"
        />
      </BaseStack>
    </BaseCard>
    <BaseCard as="section" aria-labelledby="initiative-status" :padding="6">
      <BaseStack :gap="4">
        <BaseHeading id="initiative-status" :level="2" size="lg">
          Status
        </BaseHeading>
        <FeaturesInitiativesStatusPanel
          :initiative="initiative"
          :can-change="access.isPlatformAdmin.value"
          @changed="set($event)"
        />
      </BaseStack>
    </BaseCard>
  </BaseStack>
</template>
