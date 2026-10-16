<script setup lang="ts">
import type { Grant } from '~/types/api'
import type { TableColumn } from '~/types/ui'

/**
 * Grants of an initiative: subject (user or group), role, expiry and creation date. An expired grant is shown
 * with an "Expired" badge (the API lists them). With `canRevoke` an actions column offers "Revoke"; the panel owns
 * the confirmation and the request.
 */
const props = defineProps<{
  grants: Grant[]
  canRevoke: boolean
  caption: string
}>()

const emit = defineEmits<{ revoke: [grant: Grant] }>()

const columns = computed<TableColumn[]>(() => [
  { key: 'subject', label: 'Subject' },
  { key: 'role', label: 'Role' },
  { key: 'expires_at', label: 'Expires' },
  { key: 'granted_at', label: 'Created' },
  ...(props.canRevoke ? [{ key: 'actions', label: 'Actions', align: 'end' as const, hideLabel: true }] : []),
])

// Reference time for the badge, refreshed every time the list changes (reload, load more, revoke); the badge is
// informational, the API decides access.
const now = ref(Date.now())
watch(() => props.grants, () => {
  now.value = Date.now()
})
</script>

<template>
  <BaseTable
    :caption="caption"
    caption-hidden
    :columns="columns"
    :rows="grants"
    :row-key="(row: Grant) => row.id"
    row-header="subject"
    empty-text="No grants yet."
  >
    <template #cell="{ row, column }">
      <template v-if="column.key === 'subject'">
        <BaseStack v-if="row.user" :gap="0">
          <BaseText as="span" size="sm" weight="medium">
            {{ row.user.name || row.user.email }}
          </BaseText>
          <BaseText v-if="row.user.name" as="span" size="xs" tone="muted">
            {{ row.user.email }}
          </BaseText>
        </BaseStack>
        <BaseStack v-else-if="row.group" :gap="0">
          <BaseText as="span" size="sm" weight="medium">
            {{ row.group.name || row.group.key }}
          </BaseText>
          <BaseText as="span" size="xs" tone="muted">
            Group <code class="font-mono">{{ row.group.key }}</code>
          </BaseText>
        </BaseStack>
      </template>
      <BaseCluster v-else-if="column.key === 'role'" :gap="2">
        <BaseText as="span" size="sm">
          <code class="font-mono">{{ row.role.key }}</code>
        </BaseText>
        <BaseBadge v-if="row.role.manages_access" tone="primary">
          Manages access
        </BaseBadge>
      </BaseCluster>
      <template v-else-if="column.key === 'expires_at'">
        <BaseText v-if="!row.expires_at" as="span" size="sm" tone="muted">
          Never
        </BaseText>
        <BaseCluster v-else :gap="2">
          <BaseText as="span" size="sm">
            {{ formatTimestamp(row.expires_at) }}
          </BaseText>
          <BaseBadge v-if="isPast(row.expires_at, now)" tone="warning">
            Expired
          </BaseBadge>
        </BaseCluster>
      </template>
      <BaseText v-else-if="column.key === 'granted_at'" as="span" size="sm">
        {{ formatTimestamp(row.granted_at) }}
      </BaseText>
      <BaseCluster v-else-if="column.key === 'actions'" justify="end">
        <BaseButton
          size="sm"
          variant="danger"
          :aria-label="`Revoke ${row.role.key} from ${row.user ? row.user.email : row.group?.key}`"
          @click="emit('revoke', row)"
        >
          Revoke
        </BaseButton>
      </BaseCluster>
    </template>
  </BaseTable>
</template>
