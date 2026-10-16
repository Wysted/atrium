<script setup lang="ts">
import type { App, Deployment } from '~/types/api'
import type { TableColumn } from '~/types/ui'

/**
 * Apps of an initiative: name (id underneath when named), status and hosts. Each host is an external link built
 * by `hostUrl()` (a value that fails the hostname check renders as plain text, never as a link). With `canEdit`
 * (platform admins) every host has a remove button and an actions column offers rename, disable/enable and add
 * host; the panel owns the dialogs and the requests. `busyId` marks the app whose request is running.
 * Each host shows the stored result of its last probe (`FeaturesHealthProbeBadge`) and each app links to its health
 * page. The name cell is a focus target (`id="app-<id>"`, `tabindex="-1"`) so the panel can move focus to a new row.
 */
const props = defineProps<{
  apps: App[]
  canEdit: boolean
  caption: string
  busyId?: string
}>()

const emit = defineEmits<{
  rename: [app: App]
  toggle: [app: App]
  addHost: [app: App]
  removeHost: [app: App, deployment: Deployment]
}>()

const columns = computed<TableColumn[]>(() => [
  { key: 'name', label: 'App' },
  { key: 'status', label: 'Status' },
  { key: 'hosts', label: 'Hosts' },
  { key: 'health', label: 'Health' },
  ...(props.canEdit ? [{ key: 'actions', label: 'Actions', align: 'end' as const, hideLabel: true }] : []),
])
</script>

<template>
  <BaseTable
    :caption="caption"
    caption-hidden
    :columns="columns"
    :rows="apps"
    :row-key="(row: App) => row.id"
    row-header="name"
    empty-text="No apps yet."
  >
    <template #cell="{ row, column }">
      <BaseStack v-if="column.key === 'name'" :id="`app-${row.id}`" tabindex="-1" :gap="0" class="focus-visible:outline-2 focus-visible:outline-focus">
        <BaseText as="span" size="sm" weight="medium">
          {{ appLabel(row) }}
        </BaseText>
        <BaseText v-if="row.name" as="span" size="xs" tone="muted">
          <code class="font-mono">{{ row.id }}</code>
        </BaseText>
      </BaseStack>
      <FeaturesAppsStatusBadge v-else-if="column.key === 'status'" :status="row.status" />
      <template v-else-if="column.key === 'hosts'">
        <BaseStack v-if="row.deployments.length" as="ul" :gap="1">
          <BaseBox v-for="d in row.deployments" :key="d.id" as="li">
            <BaseCluster :gap="2">
              <BaseLink v-if="hostUrl(d.host)" :href="hostUrl(d.host)!" target="_blank">
                {{ d.host }}
                <BaseText as="span" size="xs" class="sr-only">
                  (opens in a new tab)
                </BaseText>
                <BaseIcon name="external-link" size="sm" />
              </BaseLink>
              <BaseText v-else as="span" size="sm">
                {{ d.host }}
              </BaseText>
              <FeaturesHealthProbeBadge :status="d.last_probe_status" />
              <BaseText v-if="d.last_probe_at" as="time" size="xs" tone="muted" :datetime="d.last_probe_at" :title="formatTimestamp(d.last_probe_at)">
                {{ formatRelative(d.last_probe_at) }}
              </BaseText>
              <BaseButton
                v-if="canEdit"
                size="sm"
                variant="ghost"
                :disabled="busyId === row.id"
                :aria-label="`Remove host ${d.host}`"
                @click="emit('removeHost', row, d)"
              >
                <BaseIcon name="x" size="sm" />
              </BaseButton>
            </BaseCluster>
          </BaseBox>
        </BaseStack>
        <BaseText v-else size="sm" tone="muted">
          No hosts
        </BaseText>
      </template>
      <BaseLink
        v-else-if="column.key === 'health'"
        :to="`/admin/initiatives/${encodeURIComponent(row.initiative_id)}/apps/${encodeURIComponent(row.id)}/health`"
        :aria-label="`Health of app ${appLabel(row)}`"
      >
        Health
      </BaseLink>
      <BaseCluster v-else-if="column.key === 'actions'" justify="end" :gap="2">
        <BaseButton
          size="sm"
          variant="secondary"
          :disabled="busyId === row.id"
          :aria-label="`Add a host to app ${appLabel(row)}`"
          @click="emit('addHost', row)"
        >
          Add host
        </BaseButton>
        <BaseButton
          size="sm"
          variant="secondary"
          :disabled="busyId === row.id"
          :aria-label="`Rename app ${appLabel(row)}`"
          @click="emit('rename', row)"
        >
          Rename
        </BaseButton>
        <BaseButton
          size="sm"
          :variant="row.status === 'active' ? 'danger' : 'secondary'"
          :disabled="busyId === row.id"
          :aria-label="`${row.status === 'active' ? 'Disable' : 'Enable'} app ${appLabel(row)}`"
          @click="emit('toggle', row)"
        >
          {{ row.status === 'active' ? 'Disable' : 'Enable' }}
        </BaseButton>
      </BaseCluster>
    </template>
  </BaseTable>
</template>
