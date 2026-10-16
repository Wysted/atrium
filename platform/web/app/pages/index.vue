<script setup lang="ts">
useHead({ title: 'Home - Portal' })
</script>

<template>
  <BaseStack :gap="6">
    <BaseStack :gap="2">
      <BaseHeading :level="1">
        Initiatives portal
      </BaseHeading>
      <BaseText tone="muted">
        The portal is being set up. Screens will appear here soon.
      </BaseText>
    </BaseStack>

    <BaseCluster :gap="3">
      <BaseButton variant="primary">
        Primary
      </BaseButton>
      <BaseButton variant="secondary">
        Secondary
      </BaseButton>
      <BaseButton variant="ghost">
        Ghost
      </BaseButton>
      <BaseButton variant="danger">
        Danger
      </BaseButton>
      <BaseButton disabled>
        Disabled
      </BaseButton>
      <BaseButton loading>
        Loading
      </BaseButton>
    </BaseCluster>

    <BaseGrid min-column="md">
      <BaseBox surface="raised" bordered rounded="md" :padding="4">
        <BaseStack :gap="2">
          <BaseHeading :level="2" size="lg">
            Navigation
          </BaseHeading>
          <BaseText size="sm" tone="muted">
            Internal links use the router.
          </BaseText>
          <BaseLink to="/">
            Back to start
          </BaseLink>
        </BaseStack>
      </BaseBox>
      <BaseBox surface="raised" bordered rounded="md" :padding="4">
        <BaseStack :gap="2">
          <BaseHeading :level="2" size="lg">
            Documentation
          </BaseHeading>
          <BaseText size="sm" tone="muted">
            Plain links open safely in a new tab.
          </BaseText>
          <BaseCluster :gap="1">
            <BaseLink href="/docs/" target="_blank">Docs</BaseLink>
            <BaseIcon name="external-link" size="sm" />
          </BaseCluster>
        </BaseStack>
      </BaseBox>
    </BaseGrid>
  </BaseStack>
</template>
