<script setup lang="ts">
useHead({ title: 'API tokens - Portal' })

const access = useAdminAccess()
</script>

<template>
  <BaseStack :gap="6">
    <BasePageHeader
      title="API tokens"
      description="Personal tokens let scripts and the command line call the portal API as you. A token never has more permissions than you do, and stops working when you are disabled."
    >
      <BaseButton to="/" variant="secondary">
        Back to home
      </BaseButton>
    </BasePageHeader>
    <FeaturesTokensPanel
      path="/me/tokens"
      revoke-base="/me/tokens"
      owner="you"
      can-create
      :offer-admin="access.canAdminister.value"
    />
  </BaseStack>
</template>
