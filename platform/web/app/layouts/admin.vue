<script setup lang="ts">
/**
 * Layout of the administration area. It resolves /me (re-reading it in the background on mount, so permissions are not stale) and gates the page: while loading it shows a
 * placeholder, on failure the load error, and for a user who administers nothing the fixed "no access" state.
 * The page (slot) only mounts for an admin or a manager, so it never fires admin calls for anyone else.
 * The API enforces every call; this gate and the nav only hide.
 */
const access = useAdminAccess()
onMounted(() => {
  void access.revalidate()
})
const loading = computed(() => !access.me.value && !access.error.value)
</script>

<template>
  <BaseBox as="div" surface="base">
    <BaseBox as="header" bordered :padding="4">
      <BaseContainer>
        <BaseStack :gap="3">
          <BaseCluster :gap="2">
            <NuxtLink to="/" class="rounded-sm text-lg font-semibold text-text no-underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus">
              Portal
            </NuxtLink>
            <BaseText as="span" size="lg" tone="muted">
              / Administration
            </BaseText>
          </BaseCluster>
          <FeaturesAdminNav
            v-if="access.canAdminister.value"
            :is-platform-admin="access.isPlatformAdmin.value"
            :is-manager="access.isManager.value"
          />
        </BaseStack>
      </BaseContainer>
    </BaseBox>
    <BaseBox as="main" :padding="6">
      <BaseContainer>
        <BaseBox v-if="loading" as="div" aria-busy="true">
          <BaseText class="sr-only">
            Loading
          </BaseText>
          <BaseStack :gap="3">
            <BaseSkeleton height="lg" width="half" />
            <BaseSkeleton />
          </BaseStack>
        </BaseBox>
        <FeaturesHomeLoadError
          v-else-if="access.error.value"
          :error="access.error.value"
          :retrying="access.pending.value"
          subject="administration"
          forbidden-description="Your account is not allowed to use the portal."
          @retry="access.refresh()"
        />
        <FeaturesAdminNoAccess v-else-if="!access.canAdminister.value" />
        <slot v-else />
      </BaseContainer>
    </BaseBox>
  </BaseBox>
</template>
