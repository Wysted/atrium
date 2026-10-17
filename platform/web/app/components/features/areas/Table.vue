<script setup lang="ts">
import type { Area } from '~/types/api'
import type { TableColumn } from '~/types/ui'

/** Areas as a table: key (linking to the area), name and a "Delete" action; the panel owns the confirmation. */
defineProps<{
  areas: Area[]
  caption: string
}>()

const emit = defineEmits<{ remove: [area: Area] }>()

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
    :rows="areas"
    :row-key="(row: Area) => row.id"
    row-header="key"
    empty-text="No areas yet."
  >
    <template #cell="{ row, column }">
      <BaseLink v-if="column.key === 'key'" :to="`/admin/areas/${encodeURIComponent(row.id)}`">
        <code class="font-mono">{{ row.key }}</code>
      </BaseLink>
      <BaseText v-else-if="column.key === 'name'" as="span" size="sm" :tone="row.name ? 'default' : 'muted'">
        {{ row.name || 'No name' }}
      </BaseText>
      <BaseCluster v-else-if="column.key === 'actions'" justify="end">
        <BaseButton size="sm" variant="danger" :aria-label="`Delete area ${row.key}`" @click="emit('remove', row)">
          Delete
        </BaseButton>
      </BaseCluster>
    </template>
  </BaseTable>
</template>
