<script setup lang="ts">
import type { Deployment, HealthProblem } from '~/types/api'
import type { TableColumn } from '~/types/ui'

/**
 * Hosts of an app with the telemetry the API stores (last seen, last probe time and result) and the findings the
 * API reported for each host. A host without findings says "No findings", not "healthy": the verdict is the API's.
 */
const props = defineProps<{
  deployments: Deployment[]
  problems: HealthProblem[]
  caption: string
}>()

const columns: TableColumn[] = [
  { key: 'host', label: 'Host' },
  { key: 'last_seen_at', label: 'Last seen' },
  { key: 'probe', label: 'Last probe' },
  { key: 'findings', label: 'Findings' },
]

const byHost = computed(() => {
  const map = new Map<string, HealthProblem[]>()
  for (const p of props.problems) {
    if (p.host === null) continue
    const list = map.get(p.host) ?? []
    list.push(p)
    map.set(p.host, list)
  }
  return map
})
</script>

<template>
  <BaseTable
    :caption="caption"
    caption-hidden
    :columns="columns"
    :rows="deployments"
    :row-key="(row: Deployment) => row.id"
    row-header="host"
    empty-text="This app has no hosts."
  >
    <template #cell="{ row, column }">
      <BaseText v-if="column.key === 'host'" as="span" size="sm" weight="medium">
        {{ row.host }}
      </BaseText>
      <FeaturesHealthTimestamp v-else-if="column.key === 'last_seen_at'" :value="row.last_seen_at" empty-text="Never" />
      <BaseStack v-else-if="column.key === 'probe'" :gap="1" align="start">
        <FeaturesHealthProbeBadge :status="row.last_probe_status" />
        <FeaturesHealthTimestamp :value="row.last_probe_at" empty-text="No probe yet" />
      </BaseStack>
      <template v-else-if="column.key === 'findings'">
        <FeaturesHealthProblemList v-if="byHost.get(row.host)?.length" :problems="byHost.get(row.host)!" />
        <BaseText v-else as="span" size="sm" tone="muted">
          No findings
        </BaseText>
      </template>
    </template>
  </BaseTable>
</template>
