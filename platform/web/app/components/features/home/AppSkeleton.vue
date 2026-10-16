<script setup lang="ts">
/**
 * Loading placeholder for the apps grid. The announcement comes from the persistent live region that wraps
 * the results (`aria-busy` on the page); the text here is only read once that region stops being busy.
 */
</script>

<template>
  <BaseGrid as="div" min-column="md">
    <BaseText as="span" class="sr-only">
      Loading your apps
    </BaseText>
    <BaseCard v-for="n in 3" :key="n">
      <BaseStack :gap="3">
        <BaseSkeleton height="lg" width="half" />
        <BaseSkeleton height="sm" />
        <BaseSkeleton height="sm" width="half" />
      </BaseStack>
    </BaseCard>
  </BaseGrid>
</template>
