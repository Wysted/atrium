<script setup lang="ts">
import type { OrphanHost } from '~/types/api'
import type { TableColumn } from '~/types/ui'

/** Orphan hosts: host (plain text, never a link: it is an unknown name), recorded denials and the latest one. */
defineProps<{
  hosts: OrphanHost[]
  caption: string
}>()

const columns: TableColumn[] = [
  { key: 'host', label: 'Host' },
  { key: 'count', label: 'Recorded denials', align: 'end' },
  { key: 'last_denied_at', label: 'Last denied' },
]
</script>

<template>
  <BaseTable
    :caption="caption"
    caption-hidden
    :columns="columns"
    :rows="hosts"
    :row-key="(row: OrphanHost) => row.host"
    row-header="host"
    empty-text="No orphan hosts in the last 7 days."
  >
    <template #cell="{ row, column }">
      <BaseText v-if="column.key === 'host'" as="span" size="sm" weight="medium">
        <code class="font-mono break-all">{{ row.host }}</code>
      </BaseText>
      <BaseText v-else-if="column.key === 'count'" as="span" size="sm">
        {{ row.count }}
      </BaseText>
      <FeaturesHealthTimestamp v-else-if="column.key === 'last_denied_at'" :value="row.last_denied_at" empty-text="Unknown" />
    </template>
  </BaseTable>
</template>
