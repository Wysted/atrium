<script setup lang="ts">
import type { Role } from '~/types/api'
import type { TableColumn } from '~/types/ui'

/**
 * Roles of an initiative: key and whether the role manages access. With `canEdit` an actions column offers
 * toggling `manages_access` and deleting; the panel owns the requests and the dialogs. `busyId` marks the role
 * whose request is running.
 */
const props = defineProps<{
  roles: Role[]
  canEdit: boolean
  caption: string
  busyId?: string
}>()

const emit = defineEmits<{ toggle: [role: Role], remove: [role: Role] }>()

const columns = computed<TableColumn[]>(() => [
  { key: 'key', label: 'Key' },
  { key: 'manages_access', label: 'Access' },
  ...(props.canEdit ? [{ key: 'actions', label: 'Actions', align: 'end' as const, hideLabel: true }] : []),
])
</script>

<template>
  <BaseTable
    :caption="caption"
    caption-hidden
    :columns="columns"
    :rows="roles"
    :row-key="(row: Role) => row.id"
    row-header="key"
    empty-text="No roles yet."
  >
    <template #cell="{ row, column }">
      <BaseText v-if="column.key === 'key'" as="span" size="sm">
        <code class="font-mono">{{ row.key }}</code>
      </BaseText>
      <BaseBadge v-else-if="column.key === 'manages_access'" :tone="row.manages_access ? 'primary' : 'neutral'">
        {{ row.manages_access ? 'Manages access' : 'No management' }}
      </BaseBadge>
      <BaseCluster v-else-if="column.key === 'actions'" justify="end" :gap="2">
        <BaseButton
          size="sm"
          variant="secondary"
          :disabled="busyId === row.id"
          :aria-label="`${row.manages_access ? 'Remove management from' : 'Allow management for'} role ${row.key}`"
          @click="emit('toggle', row)"
        >
          {{ row.manages_access ? 'Remove management' : 'Allow management' }}
        </BaseButton>
        <BaseButton
          size="sm"
          variant="danger"
          :disabled="busyId === row.id"
          :aria-label="`Delete role ${row.key}`"
          @click="emit('remove', row)"
        >
          Delete
        </BaseButton>
      </BaseCluster>
    </template>
  </BaseTable>
</template>
