<script setup lang="ts">
import type { Robot } from '~/types/api'
import type { TableColumn } from '~/types/ui'

/**
 * Robots (platform admins). The owner reads "You" for the caller, else the owner's email (name as fallback), "None" when unset. The panel owns the dialogs and requests.
 */
defineProps<{
  robots: Robot[]
  selfId: string | null
  caption: string
  busyId?: string | null
}>()

const emit = defineEmits<{
  edit: [robot: Robot]
  setStatus: [robot: Robot]
}>()

const columns: TableColumn[] = [
  { key: 'name', label: 'Name' },
  { key: 'status', label: 'Status' },
  { key: 'owner', label: 'Owner' },
  { key: 'last_used_at', label: 'Last used' },
  { key: 'actions', label: 'Actions', align: 'end', hideLabel: true },
]
</script>

<template>
  <BaseTable
    :caption="caption"
    caption-hidden
    :columns="columns"
    :rows="robots"
    :row-key="(row: Robot) => row.id"
    row-header="name"
    empty-text="No robots yet."
  >
    <template #cell="{ row, column }">
      <BaseText v-if="column.key === 'name'" as="span" size="sm" weight="medium">
        {{ row.name }}
      </BaseText>
      <FeaturesUsersStatusBadge v-else-if="column.key === 'status'" :status="row.status" />
      <template v-else-if="column.key === 'owner'">
        <BaseBadge v-if="row.owner_user_id && row.owner_user_id === selfId" tone="primary">
          You
        </BaseBadge>
        <BaseText v-else-if="row.owner_user_id" as="span" size="sm">
          {{ robotOwnerLabel(row) }}
        </BaseText>
        <BaseText v-else as="span" size="sm" tone="muted">
          None
        </BaseText>
      </template>
      <BaseText v-else-if="column.key === 'last_used_at'" as="span" size="sm" :tone="row.last_used_at ? 'default' : 'muted'">
        {{ row.last_used_at ? formatTimestamp(row.last_used_at) : 'Never' }}
      </BaseText>
      <BaseCluster v-else-if="column.key === 'actions'" justify="end">
        <BaseButton size="sm" variant="secondary" :to="`/admin/robots/${row.id}`" :aria-label="`Tokens of robot ${row.name}`">
          Tokens
        </BaseButton>
        <BaseButton size="sm" variant="secondary" :disabled="busyId === row.id" :aria-label="`Edit robot ${row.name}`" @click="emit('edit', row)">
          Edit
        </BaseButton>
        <BaseButton
          size="sm"
          :variant="row.status === 'active' ? 'danger' : 'secondary'"
          :disabled="busyId === row.id"
          :aria-label="`${row.status === 'active' ? 'Disable' : 'Enable'} robot ${row.name}`"
          @click="emit('setStatus', row)"
        >
          {{ row.status === 'active' ? 'Disable' : 'Enable' }}
        </BaseButton>
      </BaseCluster>
    </template>
  </BaseTable>
</template>
