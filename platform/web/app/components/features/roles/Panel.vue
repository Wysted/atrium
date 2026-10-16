<script setup lang="ts">
import type { PatchRoleBody, Role, RoleList } from '~/types/api'

/**
 * Roles screen of an initiative. Everyone who can open the initiative reads the list; with `canEdit` (platform
 * admins; the API enforces) it also creates roles, toggles `manages_access` and deletes roles.
 * - Turning `manages_access` OFF asks for confirmation (it removes the role's power to manage access; 409 = last
 *   effective manager). Turning it ON is harmless and goes straight through.
 * - Deleting asks for confirmation; 409 = the role still has grants.
 * Fixed copy only, never server detail. The API does not return a grant count per role.
 */
const props = defineProps<{
  initiativeId: string
  canEdit: boolean
}>()

const api = useApi()
const path = computed(() => `/initiatives/${encodeURIComponent(props.initiativeId)}/roles`)
const list = usePagedList<RoleList, Role>(path.value, page => page.roles)

const createOpen = ref(false)

type Pending = { kind: 'disable' | 'delete', role: Role }
const pending = ref<Pending | null>(null)
const confirmOpen = computed({
  get: () => pending.value !== null,
  set: (value: boolean) => {
    if (!value) pending.value = null
  },
})
const confirmBusy = ref(false)
const confirmError = ref('')
const rowBusyId = ref('')
const notice = ref('')
const actionError = ref('')

function replaceRole(role: Role) {
  list.items.value = list.items.value.map(r => (r.id === role.id ? role : r))
}

function ask(kind: Pending['kind'], role: Role) {
  confirmError.value = ''
  pending.value = { kind, role }
}

async function setManagesAccess(role: Role, value: boolean): Promise<Role> {
  const body: PatchRoleBody = { manages_access: value }
  return await api.patch<Role>(`/roles/${encodeURIComponent(role.id)}`, body)
}

async function onToggle(role: Role) {
  notice.value = ''
  actionError.value = ''
  if (role.manages_access) {
    ask('disable', role)
    return
  }
  rowBusyId.value = role.id
  try {
    replaceRole(await setManagesAccess(role, true))
    notice.value = `Role ${role.key} now manages access.`
  }
  catch (e) {
    actionError.value = describeMutationError(e, {
      conflict: 'The role changed in the meantime. Reload the page and try again.',
      notFound: 'This role no longer exists. Reload the page.',
    }).message
  }
  finally {
    rowBusyId.value = ''
  }
}

async function confirm() {
  const current = pending.value
  if (!current || confirmBusy.value) return
  confirmBusy.value = true
  confirmError.value = ''
  try {
    if (current.kind === 'disable') {
      replaceRole(await setManagesAccess(current.role, false))
      notice.value = `Role ${current.role.key} no longer manages access.`
    }
    else {
      await api.del(`/roles/${encodeURIComponent(current.role.id)}`)
      list.items.value = list.items.value.filter(r => r.id !== current.role.id)
      notice.value = `Role ${current.role.key} deleted.`
    }
    pending.value = null
  }
  catch (e) {
    confirmError.value = describeMutationError(e, {
      conflict: current.kind === 'disable' ? LAST_MANAGER_COPY : 'Remove this role\'s grants first.',
      notFound: 'This role no longer exists. Reload the page.',
    }).message
  }
  finally {
    confirmBusy.value = false
  }
}

function onCreated(role: Role) {
  notice.value = `Role ${role.key} created.`
  void list.reload()
}

const confirmTitle = computed(() => {
  const current = pending.value
  if (!current) return ''
  return current.kind === 'disable' ? `Remove management from ${current.role.key}?` : `Delete role ${current.role.key}?`
})
const confirmDescription = computed(() =>
  pending.value?.kind === 'disable'
    ? 'Holders of this role will no longer be able to grant or revoke access in this initiative. Their grants stay, without the management power.'
    : 'The role is removed from the initiative. Apps can no longer receive it. A role that still has grants cannot be deleted.',
)
</script>

<template>
  <BaseStack :gap="4">
    <BaseCluster justify="between">
      <BaseHeading :level="2" size="lg">
        Roles
      </BaseHeading>
      <BaseButton v-if="canEdit" @click="createOpen = true">
        <BaseIcon name="plus" size="sm" />
        New role
      </BaseButton>
    </BaseCluster>
    <BaseText v-if="!canEdit" size="sm" tone="muted">
      Only platform administrators change roles. You need them to grant access.
    </BaseText>

    <BaseText role="status" tone="success" size="sm" :class="notice ? undefined : 'sr-only'">
      {{ notice }}
    </BaseText>
    <BaseText v-if="actionError" role="alert" tone="danger" size="sm">
      {{ actionError }}
    </BaseText>

    <BaseBox as="div" :aria-busy="list.loading.value">
      <BaseStack v-if="list.loading.value" :gap="3">
        <BaseText class="sr-only">
          Loading roles
        </BaseText>
        <BaseSkeleton />
        <BaseSkeleton />
      </BaseStack>
      <FeaturesHomeLoadError
        v-else-if="list.error.value && list.items.value.length === 0"
        :error="list.error.value"
        subject="roles"
        forbidden-description="Your account is not allowed to see the roles of this initiative."
        @retry="list.reload()"
      />
      <BaseStack v-else :gap="4">
        <FeaturesRolesTable
          caption="Roles"
          :roles="list.items.value"
          :can-edit="canEdit"
          :busy-id="rowBusyId"
          @toggle="onToggle"
          @remove="ask('delete', $event)"
        />
        <BaseCluster v-if="list.error.value" :gap="2">
          <BaseText role="alert" size="sm" tone="danger">
            Could not load more roles.
          </BaseText>
          <BaseButton size="sm" variant="secondary" :loading="list.loadingMore.value" @click="list.loadMore()">
            Try again
          </BaseButton>
        </BaseCluster>
        <BaseCluster v-else-if="list.hasMore.value" justify="center">
          <BaseButton variant="secondary" :loading="list.loadingMore.value" @click="list.loadMore()">
            Load more
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </BaseBox>

    <template v-if="canEdit">
      <FeaturesRolesCreateDialog v-model:open="createOpen" :initiative-id="initiativeId" @created="onCreated" />
      <UiConfirmDialog
        v-model:open="confirmOpen"
        :title="confirmTitle"
        :description="confirmDescription"
        :confirm-label="pending?.kind === 'disable' ? 'Remove management' : 'Delete role'"
        :busy="confirmBusy"
        :error="confirmError"
        @confirm="confirm"
      />
    </template>
  </BaseStack>
</template>
