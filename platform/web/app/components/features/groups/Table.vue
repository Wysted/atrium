<script setup lang="ts">
import type { Group } from '~/types/api'
import type { TableColumn } from '~/types/ui'

/** Groups as a table: key (linking to the members), name and a "Delete" action; the panel owns the confirmation. */
defineProps<{
  groups: Group[]
  caption: string
  busyId?: string | null
}>()

const emit = defineEmits<{ remove: [group: Group] }>()

const columns: TableColumn[] = [
  { key: 'key', label: 'Key' },
  { key: 'name', label: 'Name' },
  { key: 'actions', label: 'Actions', align: 'end', hideLabel: true },
]
</script>

<template>
  <BaseTable
    :caption="caption"
    caption-hidden
    :columns="columns"
    :rows="groups"
    :row-key="(row: Group) => row.id"
    row-header="key"
    empty-text="No groups yet."
  >
    <template #cell="{ row, column }">
      <BaseLink v-if="column.key === 'key'" :to="`/admin/groups/${encodeURIComponent(row.id)}`">
        <code class="font-mono">{{ row.key }}</code>
      </BaseLink>
      <BaseText v-else-if="column.key === 'name'" as="span" size="sm" :tone="row.name ? 'default' : 'muted'">
        {{ row.name || 'No name' }}
      </BaseText>
      <BaseCluster v-else-if="column.key === 'actions'" justify="end">
        <BaseButton
          size="sm"
          variant="danger"
          :disabled="busyId === row.id"
          :aria-label="`Delete group ${row.key}`"
          @click="emit('remove', row)"
        >
          Delete
        </BaseButton>
      </BaseCluster>
    </template>
  </BaseTable>
</template>
