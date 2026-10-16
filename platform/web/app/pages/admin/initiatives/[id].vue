<script setup lang="ts">
import type { Initiative } from '~/types/api'

// Parent route of the initiative screens (overview, grants, roles): it loads the initiative once, shows the title
// and the sub-navigation, and hands the initiative to the nested pages.
definePageMeta({ layout: 'admin' })

const route = useRoute()
const id = String(route.params.id)
const access = useAdminAccess()

// The key includes the id, so a different initiative never shows another one's cached data.
const state = useApiData<Initiative>(`initiative-${id}`, `/initiatives/${encodeURIComponent(id)}`)

const initiative = state.data
const title = computed(() => (initiative.value ? initiativeLabel(initiative.value) : 'Initiative'))
useHead({ title: computed(() => `${title.value} - Initiatives - Administration - Portal`) })

const notFound = computed(() => state.error.value?.kind === 'not_found')
const loading = computed(() => !initiative.value && !state.error.value)

provideInitiativeContext({
  // Only the nested pages read it, and they render once the initiative is loaded.
  initiative: initiative as Ref<Initiative>,
  set: value => state.set(value),
})
</script>

<template>
  <BaseStack :gap="6">
    <BaseLink to="/admin/initiatives">
      Back to initiatives
    </BaseLink>

    <BaseBox v-if="loading" as="div" aria-busy="true">
      <BaseText class="sr-only">
        Loading initiative
      </BaseText>
      <BaseStack :gap="3">
        <BaseSkeleton height="lg" width="half" />
        <BaseSkeleton />
        <BaseSkeleton />
      </BaseStack>
    </BaseBox>

    <BaseEmptyState
      v-else-if="notFound"
      title="Initiative not found"
      description="It does not exist, or you do not manage it."
    >
      <BaseButton to="/admin/initiatives" variant="secondary">
        Back to initiatives
      </BaseButton>
    </BaseEmptyState>

    <FeaturesHomeLoadError
      v-else-if="state.error.value"
      :error="state.error.value"
      :retrying="state.pending.value"
      subject="this initiative"
      forbidden-description="Your account is not allowed to see this initiative."
      @retry="state.refresh()"
    />

    <template v-else-if="initiative">
      <BasePageHeader
        :title="title"
        :description="access.isPlatformAdmin.value ? 'Manage the initiative, its roles and who has access.' : 'Manage who has access to this initiative.'"
      />
      <FeaturesInitiativesSubNav :initiative-id="initiative.id" />
      <NuxtPage />
    </template>
  </BaseStack>
</template>
