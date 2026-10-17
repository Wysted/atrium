<script setup lang="ts">
/**
 * `/areas/[id]/people/[userId]`: the personal tokens of one person (revoke behind a confirmation, no "New token")
 * and the robots they own.
 */
defineProps<{ userId: string, areaId: string, label: string }>()
// The robots list answers 404 exactly when the person is out of scope (the tokens list would too).
const notFound = ref(false)
</script>

<template>
  <BaseEmptyState
    v-if="notFound"
    title="This person is not in your areas"
    description="They may have moved to another area or been removed. Go back to the area to see who you can manage."
  >
    <BaseButton :to="`/areas/${encodeURIComponent(areaId)}`">
      Back to the area
    </BaseButton>
  </BaseEmptyState>
  <BaseStack v-else :gap="8">
    <FeaturesTokensPanel
      :path="`/users/${encodeURIComponent(userId)}/tokens`"
      revoke-base="/tokens"
      :owner="label"
      :offer-admin="false"
      :can-create="false"
      :heading="`Personal tokens of ${label}`"
    />
    <FeaturesSupervisionRobotsPanel :user-id="userId" :label="label" @not-found="notFound = true" />
  </BaseStack>
</template>
