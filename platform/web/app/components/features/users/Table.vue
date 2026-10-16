<script setup lang="ts">
import type { AdminUser } from '~/types/api'
import type { TableColumn } from '~/types/ui'

/**
 * Users found by email. "Disable" is hidden for the caller's own row (`selfId`; the API still answers 409 if it
 * is sent). The panel owns the confirmations and requests.
 */
defineProps<{
  users: AdminUser[]
  selfId: string | null
  caption: string
  busyId?: string | null
}>()

const emit = defineEmits<{
  setStatus: [user: AdminUser]
  revokeSessions: [user: AdminUser]
}>()

const columns: TableColumn[] = [
  { key: 'user', label: 'User' },
  { key: 'status', label: 'Status' },
  { key: 'created_at', label: 'Created' },
  { key: 'actions', label: 'Actions', align: 'end', hideLabel: true },
]
</script>

<template>
  <BaseTable
    :caption="caption"
    caption-hidden
    :columns="columns"
    :rows="users"
    :row-key="(row: AdminUser) => row.id"
    row-header="user"
    empty-text="No user has this email."
  >
    <template #cell="{ row, column }">
      <BaseStack v-if="column.key === 'user'" :gap="0">
        <BaseCluster :gap="2">
          <BaseText as="span" size="sm" weight="medium">
            {{ row.name || row.email }}
          </BaseText>
          <BaseBadge v-if="row.id === selfId" tone="primary">
            You
          </BaseBadge>
        </BaseCluster>
        <BaseText v-if="row.name" as="span" size="xs" tone="muted">
          {{ row.email }}
        </BaseText>
      </BaseStack>
      <FeaturesUsersStatusBadge v-else-if="column.key === 'status'" :status="row.status" />
      <BaseText v-else-if="column.key === 'created_at'" as="span" size="sm">
        {{ formatTimestamp(row.created_at) }}
      </BaseText>
      <BaseCluster v-else-if="column.key === 'actions'" justify="end">
        <BaseButton
          v-if="row.status === 'disabled'"
          size="sm"
          variant="secondary"
          :disabled="busyId === row.id"
          :aria-label="`Enable ${row.email}`"
          @click="emit('setStatus', row)"
        >
          Enable
        </BaseButton>
        <BaseButton
          v-else-if="row.id !== selfId"
          size="sm"
          variant="danger"
          :disabled="busyId === row.id"
          :aria-label="`Disable ${row.email}`"
          @click="emit('setStatus', row)"
        >
          Disable
        </BaseButton>
        <BaseButton
          size="sm"
          variant="secondary"
          :disabled="busyId === row.id"
          :aria-label="`Revoke sessions of ${row.email}`"
          @click="emit('revokeSessions', row)"
        >
          Revoke sessions
        </BaseButton>
      </BaseCluster>
    </template>
  </BaseTable>
</template>
