<script setup lang="ts">
import type { AuditEvent, AuditList, Initiative, InitiativeList } from '~/types/api'
import type { SelectOption } from '~/types/ui'

/**
 * Audit screen (platform admins and managers; the API scopes it). Newest first with a keyset "Load more". Without
 * a filter a platform admin sees every event and a manager the events of their initiatives (the API decides). The
 * optional filter lists the initiatives the caller can see (`GET /initiatives`, "Load more initiatives"); a
 * manager asking for another initiative gets a 404, shown as a fixed message. Changing the filter reloads from
 * the first page. Fixed copy only; before/after render as escaped text.
 */
const access = useAdminAccess()
const initiatives = usePagedList<InitiativeList, Initiative>('/initiatives', page => page.initiatives)

const filter = ref('')
const events = usePagedList<AuditList, AuditEvent>(
  () => `/audit${filter.value ? `?initiative_id=${encodeURIComponent(filter.value)}` : ''}`,
  page => page.events,
)

watch(filter, () => {
  void events.reload()
})

const options = computed<SelectOption[]>(() => [
  { value: '', label: access.isPlatformAdmin.value ? 'All initiatives' : 'All my initiatives' },
  ...initiatives.items.value.map(i => ({ value: i.id, label: i.name ? `${i.name} (${i.key})` : i.key })),
])

const labels = computed(() => {
  const out: Record<string, string> = {}
  for (const i of initiatives.items.value) out[i.id] = initiativeLabel(i)
  return out
})

// Polite announcement after each (re)load; "load more" does not re-announce.
const announcement = ref('')
watch(events.loading, (isLoading) => {
  if (isLoading) {
    announcement.value = ''
    return
  }
  if (!events.error.value) {
    const n = events.items.value.length
    announcement.value = n === 1 ? '1 event' : `${n} events`
  }
})

const filterNotFound = computed(() => events.error.value?.kind === 'not_found')
</script>

<template>
  <BaseStack :gap="6">
    <BasePageHeader
      title="Audit"
      :description="access.isPlatformAdmin.value ? 'Every change made in the portal, newest first.' : 'Changes in the initiatives you manage, newest first.'"
    />

    <BaseStack :gap="2">
      <BaseField v-slot="field" label="Initiative">
        <BaseSelect
          v-bind="field"
          v-model="filter"
          :options="options"
          :disabled="initiatives.loading.value"
        />
      </BaseField>
      <BaseCluster v-if="initiatives.error.value" :gap="2">
        <BaseText role="alert" size="sm" tone="danger">
          Could not load the initiatives.
        </BaseText>
        <BaseButton
          size="sm"
          variant="secondary"
          :loading="initiatives.loadingMore.value || initiatives.loading.value"
          @click="initiatives.items.value.length === 0 ? initiatives.reload() : initiatives.loadMore()"
        >
          Try again
        </BaseButton>
      </BaseCluster>
      <BaseCluster v-else-if="initiatives.hasMore.value">
        <BaseButton size="sm" variant="secondary" :loading="initiatives.loadingMore.value" @click="initiatives.loadMore()">
          Load more initiatives
        </BaseButton>
      </BaseCluster>
    </BaseStack>

    <BaseText role="status" aria-live="polite" class="sr-only">
      {{ announcement }}
    </BaseText>

    <BaseBox as="div" :aria-busy="events.loading.value">
      <BaseStack v-if="events.loading.value" :gap="3">
        <BaseText class="sr-only">
          Loading audit events
        </BaseText>
        <BaseSkeleton />
        <BaseSkeleton />
        <BaseSkeleton />
      </BaseStack>
      <BaseEmptyState
        v-else-if="filterNotFound"
        title="Initiative not found"
        description="It does not exist, or you do not have access to its audit log. Choose another initiative."
      />
      <FeaturesHomeLoadError
        v-else-if="events.error.value && events.items.value.length === 0"
        :error="events.error.value"
        subject="the audit log"
        forbidden-description="Your account is not allowed to see the audit log."
        @retry="events.reload()"
      />
      <BaseStack v-else :gap="4">
        <FeaturesAuditTable caption="Audit events" :events="events.items.value" :initiative-labels="labels" />
        <BaseCluster v-if="events.error.value" :gap="2">
          <BaseText role="alert" size="sm" tone="danger">
            Could not load more events.
          </BaseText>
          <BaseButton size="sm" variant="secondary" :loading="events.loadingMore.value" @click="events.loadMore()">
            Try again
          </BaseButton>
        </BaseCluster>
        <BaseCluster v-else-if="events.hasMore.value" justify="center">
          <BaseButton variant="secondary" :loading="events.loadingMore.value" @click="events.loadMore()">
            Load more
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </BaseBox>
  </BaseStack>
</template>
