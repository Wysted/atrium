<script setup lang="ts">
import type { HomeResponse, MeResponse } from '~/types/api'

useHead({ title: 'Home - Portal' })

const me = useApiData<MeResponse>('me', '/me')
const apps = useApiData<HomeResponse>('me-apps', '/me/apps')

const displayName = computed(() => me.data.value?.user.name || me.data.value?.user.email || '')
const title = computed(() => (displayName.value ? `Welcome, ${displayName.value}` : 'Welcome'))
// The UI only hides: the API enforces access to every admin call.
const showAdmin = computed(() => {
  const permissions = me.data.value?.permissions
  return !!permissions && (permissions.platform_admin || permissions.managed_initiatives.length > 0)
})
// The page state comes from the apps request alone; /me only adds the greeting and the admin link.
const loadError = computed(() => apps.error.value)
const meFailed = computed(() => !!me.error.value && !me.pending.value && !me.error.value.reloading)
const header = useTemplateRef<{ focusTitle: () => void }>('header')

async function retry() {
  await Promise.all([me.refresh(), apps.refresh()])
  if (!apps.error.value) {
    // The error state (and the button that had focus) is gone: park focus on the heading.
    await nextTick()
    header.value?.focusTitle()
  }
}
</script>

<template>
  <BaseStack :gap="6">
    <BasePageHeader ref="header" :title="title" description="The apps you can open.">
      <BaseCluster :gap="2">
        <BaseButton to="/tokens" variant="secondary">
          API tokens
        </BaseButton>
        <BaseButton v-if="showAdmin" to="/admin" variant="secondary">
          Administration
        </BaseButton>
      </BaseCluster>
    </BasePageHeader>

    <BaseCluster v-if="meFailed && apps.data.value" :gap="2">
      <BaseText size="sm" tone="muted">
        Some details could not be loaded.
      </BaseText>
      <BaseButton size="sm" variant="ghost" :loading="me.pending.value" @click="me.refresh()">
        Try again
      </BaseButton>
    </BaseCluster>

    <!-- Persistent live region: its content swaps between skeleton, error and results. -->
    <BaseBox as="div" aria-live="polite" :aria-busy="apps.pending.value">
      <FeaturesHomeAppSkeleton v-if="apps.pending.value && !apps.data.value && !loadError" />
      <FeaturesHomeLoadError v-else-if="loadError" :error="loadError" :retrying="apps.pending.value" @retry="retry" />
      <BaseEmptyState
        v-else-if="apps.data.value && apps.data.value.apps.length === 0"
        title="No apps yet"
        description="Access to apps is granted by the administrators of each initiative. Once you are given access, your apps will show up here."
      />
      <BaseGrid v-else-if="apps.data.value" as="ul" min-column="md">
        <FeaturesHomeAppCard v-for="app in apps.data.value.apps" :key="app.id" :app="app" />
      </BaseGrid>
    </BaseBox>
  </BaseStack>
</template>
