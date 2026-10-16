<script setup lang="ts">
import type { Initiative } from '~/types/api'
import type { TableColumn } from '~/types/ui'

/** Initiatives as a table: name (the key when the name is empty) linking to the detail, key and status. */
defineProps<{
  initiatives: Initiative[]
  caption: string
}>()

const columns: TableColumn[] = [
  { key: 'name', label: 'Name' },
  { key: 'key', label: 'Key' },
  { key: 'status', label: 'Status' },
]
</script>

<template>
  <BaseTable
    :caption="caption"
    caption-hidden
    :columns="columns"
    :rows="initiatives"
    :row-key="(row: Initiative) => row.id"
    row-header="name"
    empty-text="No initiatives yet."
  >
    <template #cell="{ row, column }">
      <BaseLink v-if="column.key === 'name'" :to="`/admin/initiatives/${encodeURIComponent(row.id)}`">
        {{ initiativeLabel(row) }}
      </BaseLink>
      <BaseText v-else-if="column.key === 'key'" as="span" size="sm">
        <code class="font-mono">{{ row.key }}</code>
      </BaseText>
      <FeaturesInitiativesStatusBadge v-else-if="column.key === 'status'" :status="row.status" />
    </template>
  </BaseTable>
</template>
