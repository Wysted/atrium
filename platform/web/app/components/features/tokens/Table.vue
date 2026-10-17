<script setup lang="ts">
import type { ApiToken } from '~/types/api'
import type { TableColumn } from '~/types/ui'

/**
 * Tokens of one owner: name, scope, state (Active / Expired / Revoked), dates and last use. Revoke is offered for
 * live tokens only (an expired one is dead already); the panel owns the confirmation and the request. The secret is never part of a row.
 */
const props = defineProps<{
  tokens: ApiToken[]
  caption: string
}>()

const emit = defineEmits<{ revoke: [token: ApiToken] }>()

const columns: TableColumn[] = [
  { key: 'name', label: 'Name' },
  { key: 'scope', label: 'Scope' },
  { key: 'state', label: 'State' },
  { key: 'created_at', label: 'Created' },
  { key: 'expires_at', label: 'Expires' },
  { key: 'last_used_at', label: 'Last used' },
  { key: 'actions', label: 'Actions', align: 'end', hideLabel: true },
]

// Reference time, refreshed whenever the list changes; the badge is informational.
const now = ref(Date.now())
watch(() => props.tokens, () => {
  now.value = Date.now()
})

const STATE_COPY = {
  active: { label: 'Active', tone: 'success' },
  expired: { label: 'Expired', tone: 'warning' },
  revoked: { label: 'Revoked', tone: 'danger' },
} as const
</script>

<template>
  <BaseTable
    :caption="caption"
    caption-hidden
    :columns="columns"
    :rows="tokens"
    :row-key="(row: ApiToken) => row.id"
    row-header="name"
    empty-text="No tokens yet."
  >
    <template #cell="{ row, column }">
      <BaseText v-if="column.key === 'name'" as="span" size="sm" weight="medium">
        {{ row.name }}
      </BaseText>
      <BaseBadge v-else-if="column.key === 'scope'" :tone="row.scope === 'admin' ? 'warning' : 'neutral'">
        {{ row.scope }}
      </BaseBadge>
      <BaseBadge v-else-if="column.key === 'state'" :tone="STATE_COPY[tokenState(row, now)].tone">
        {{ STATE_COPY[tokenState(row, now)].label }}
      </BaseBadge>
      <BaseText v-else-if="column.key === 'created_at'" as="span" size="sm">
        {{ formatTimestamp(row.created_at) }}
      </BaseText>
      <BaseText v-else-if="column.key === 'expires_at'" as="span" size="sm">
        {{ formatTimestamp(row.expires_at) }}
      </BaseText>
      <BaseText v-else-if="column.key === 'last_used_at'" as="span" size="sm" :tone="row.last_used_at ? 'default' : 'muted'">
        {{ row.last_used_at ? formatTimestamp(row.last_used_at) : 'Never' }}
      </BaseText>
      <BaseCluster v-else-if="column.key === 'actions'" justify="end">
        <BaseButton
          v-if="tokenState(row, now) === 'active'"
          size="sm"
          variant="danger"
          :aria-label="`Revoke token ${row.name}`"
          @click="emit('revoke', row)"
        >
          Revoke
        </BaseButton>
      </BaseCluster>
    </template>
  </BaseTable>
</template>
