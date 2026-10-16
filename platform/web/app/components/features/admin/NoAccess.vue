<script setup lang="ts">
/** Fixed state for a signed-in user who is neither a platform admin nor an initiative manager. */
</script>

<template>
  <BaseEmptyState
    icon="info"
    title="You do not have access to administration"
    description="Administration is for platform administrators and initiative managers."
  >
    <BaseButton to="/" variant="secondary">
      Go to home
    </BaseButton>
  </BaseEmptyState>
</template>
