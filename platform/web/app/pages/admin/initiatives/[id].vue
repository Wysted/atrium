<script setup lang="ts">
import type { Initiative } from '~/types/api'

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
        :description="access.isPlatformAdmin.value ? 'Rename the initiative or change its status.' : 'You can see this initiative; only platform administrators change it.'"
      />
      <BaseCard as="section" aria-labelledby="initiative-overview" :padding="6">
        <BaseStack :gap="4">
          <BaseHeading id="initiative-overview" :level="2" size="lg">
            Overview
          </BaseHeading>
          <FeaturesInitiativesNameForm
            :initiative="initiative"
            :editable="access.isPlatformAdmin.value"
            @saved="state.set($event)"
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
            @changed="state.set($event)"
          />
        </BaseStack>
      </BaseCard>
    </template>
  </BaseStack>
</template>
