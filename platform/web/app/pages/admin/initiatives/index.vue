<script setup lang="ts">
import type { Initiative, InitiativeList } from '~/types/api'

definePageMeta({ layout: 'admin' })
useHead({ title: 'Initiatives - Administration - Portal' })

const access = useAdminAccess()
const list = usePagedList<InitiativeList, Initiative>('/initiatives', page => page.initiatives)

const createOpen = ref(false)

const description = computed(() =>
  access.isPlatformAdmin.value ? 'Create initiatives and manage their status.' : 'The initiatives you manage.',
)

async function onCreated(initiative: Initiative) {
  await navigateTo(`/admin/initiatives/${encodeURIComponent(initiative.id)}`)
}
</script>

<template>
  <BaseStack :gap="6">
    <BasePageHeader title="Initiatives" :description="description">
      <BaseButton v-if="access.isPlatformAdmin.value" @click="createOpen = true">
        <BaseIcon name="plus" size="sm" />
        New initiative
      </BaseButton>
    </BasePageHeader>

    <BaseBox as="div" :aria-busy="list.loading.value">
      <BaseStack v-if="list.loading.value" :gap="3">
        <BaseText class="sr-only">
          Loading initiatives
        </BaseText>
        <BaseSkeleton />
        <BaseSkeleton />
        <BaseSkeleton />
      </BaseStack>
      <FeaturesHomeLoadError
        v-else-if="list.error.value && list.items.value.length === 0"
        :error="list.error.value"
        subject="initiatives"
        forbidden-description="Your account is not allowed to see initiatives."
        @retry="list.reload()"
      />
      <BaseStack v-else :gap="4">
        <FeaturesInitiativesTable caption="Initiatives" :initiatives="list.items.value" />
        <BaseCluster v-if="list.error.value" :gap="2">
          <BaseText role="alert" size="sm" tone="danger">
            Could not load more initiatives.
          </BaseText>
          <BaseButton size="sm" variant="secondary" :loading="list.loadingMore.value" @click="list.loadMore()">
            Try again
          </BaseButton>
        </BaseCluster>
        <BaseCluster v-else-if="list.hasMore.value" justify="center">
          <BaseButton variant="secondary" :loading="list.loadingMore.value" @click="list.loadMore()">
            Load more
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </BaseBox>

    <FeaturesInitiativesCreateDialog v-if="access.isPlatformAdmin.value" v-model:open="createOpen" @created="onCreated" />
  </BaseStack>
</template>
