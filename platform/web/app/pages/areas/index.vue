<script setup lang="ts">
useHead({ title: 'My areas - Portal' })

const access = useAdminAccess()
</script>

<template>
  <BaseStack :gap="6">
    <BasePageHeader title="My areas" description="The areas you supervise.">
      <BaseButton to="/" variant="secondary">
        Back to home
      </BaseButton>
    </BasePageHeader>
    <FeaturesSupervisionGate>
      <FeaturesSupervisionAreasPanel :areas="access.supervisedAreas.value" />
    </FeaturesSupervisionGate>
  </BaseStack>
</template>
