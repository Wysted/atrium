<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import type { App, AppList, Deployment, PatchAppBody } from '~/types/api'

/**
 * Apps screen of an initiative. Everyone who can open the initiative reads the list (apps with their hosts);
 * with `canEdit` (platform admins; the API enforces) it also creates, renames, disables/enables apps and adds or
 * removes hosts.
 * - Creating an app is NOT idempotent: see `FeaturesAppsCreateDialog`. After an ambiguous failure the list is
 *   reloaded and a fixed "may have been created" alert stays until the next successful create.
 * - Disabling and removing a host ask for confirmation. The portal app cannot be told apart from the payload
 *   (no initiative key, no flag), so the Disable and Remove actions are always offered and the 409s carry fixed
 *   copy ("The portal app cannot be disabled." / "The portal app needs at least one host.").
 * Fixed copy only, never server detail.
 */
const props = defineProps<{
  initiativeId: string
  canEdit: boolean
}>()

const api = useApi()
const path = computed(() => `/initiatives/${encodeURIComponent(props.initiativeId)}/apps`)
const list = usePagedList<AppList, App>(path.value, page => page.apps)

const createOpen = ref(false)
const renameOpen = ref(false)
const renameTarget = ref<App | null>(null)
const hostOpen = ref(false)
const hostTarget = ref<App | null>(null)

type Pending = { kind: 'disable', app: App } | { kind: 'remove-host', app: App, deployment: Deployment }
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
const uncertain = ref('')

function clearMessages() {
  notice.value = ''
  actionError.value = ''
}

function replaceApp(app: App) {
  list.items.value = list.items.value.map(a => (a.id === app.id ? app : a))
}

function openRename(app: App) {
  clearMessages()
  renameTarget.value = app
  renameOpen.value = true
}

function openAddHost(app: App) {
  clearMessages()
  hostTarget.value = app
  hostOpen.value = true
}

function ask(next: Pending) {
  clearMessages()
  confirmError.value = ''
  pending.value = next
}

async function onToggle(app: App) {
  if (app.status === 'active') {
    ask({ kind: 'disable', app })
    return
  }
  clearMessages()
  rowBusyId.value = app.id
  try {
    const body: PatchAppBody = { status: 'active' }
    replaceApp(await api.patch<App>(`/apps/${encodeURIComponent(app.id)}`, body))
    notice.value = `App ${appLabel(app)} enabled.`
  }
  catch (e) {
    if (e instanceof ApiError && (e.kind === 'not_found' || e.kind === 'conflict')) {
      await resync(APP_CHANGED_COPY)
      return
    }
    actionError.value = describeMutationError(e, {
      conflict: 'The app changed in the meantime. Reload the page and try again.',
      notFound: 'This app no longer exists. Reload the page.',
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
      const body: PatchAppBody = { status: 'disabled' }
      replaceApp(await api.patch<App>(`/apps/${encodeURIComponent(current.app.id)}`, body))
      notice.value = `App ${appLabel(current.app)} disabled.`
    }
    else {
      await api.del(`/deployments/${encodeURIComponent(current.deployment.id)}`)
      replaceApp({ ...current.app, deployments: current.app.deployments.filter(d => d.id !== current.deployment.id) })
      notice.value = `Host ${current.deployment.host} removed.`
    }
    pending.value = null
  }
  catch (e) {
    if (e instanceof ApiError && e.kind === 'not_found') {
      pending.value = null
      await resync(current.kind === 'disable' ? APP_CHANGED_COPY : HOST_GONE_COPY)
      return
    }
    confirmError.value = describeMutationError(e, {
      conflict: current.kind === 'disable' ? PORTAL_APP_DISABLE_COPY : PORTAL_APP_LAST_HOST_COPY,
      notFound: current.kind === 'disable'
        ? 'This app no longer exists. Reload the page.'
        : 'This host no longer exists. Reload the page.',
    }).message
  }
  finally {
    confirmBusy.value = false
  }
}

const APP_CHANGED_COPY = 'This app changed in the meantime.'
const HOST_GONE_COPY = 'This host no longer exists.'

/** The state moved under us: reload the list without the skeleton and say so with fixed copy. */
async function resync(message: string) {
  await list.reload(true)
  notice.value = message
}

function onRenamed(app: App) {
  replaceApp(app)
  notice.value = `App renamed to ${appLabel(app)}.`
}

function onHostAdded(deployment: Deployment) {
  const app = list.items.value.find(a => a.id === deployment.app_id)
  if (!app) return
  // The API orders deployments by host.
  const deployments = [...app.deployments.filter(d => d.id !== deployment.id), deployment]
    .sort((a, b) => (a.host < b.host ? -1 : a.host > b.host ? 1 : 0))
  replaceApp({ ...app, deployments })
  notice.value = `Host ${deployment.host} added.`
}

async function onCreated(app: App) {
  uncertain.value = ''
  clearMessages()
  // Oldest first: a new app belongs at the end. Without more pages it is appended and focused; otherwise it sits
  // on the last page, which "Load more" reaches.
  if (list.hasMore.value) {
    notice.value = `App ${appLabel(app)} created. It is at the end of the list: use Load more.`
    return
  }
  list.items.value = [...list.items.value.filter(a => a.id !== app.id), app]
  notice.value = `App ${appLabel(app)} created.`
  await nextTick()
  document.getElementById(`app-${app.id}`)?.focus()
}

async function onUncertain() {
  clearMessages()
  uncertain.value = APP_CREATE_AMBIGUOUS_COPY
  await list.reload()
}

async function reloadList() {
  await list.reload()
}

const removingCurrentHost = computed(() =>
  pending.value?.kind === 'remove-host' && isCurrentHost(pending.value.deployment.host),
)

const confirmTitle = computed(() => {
  const current = pending.value
  if (!current) return ''
  if (removingCurrentHost.value) return 'Remove the host you are using?'
  return current.kind === 'disable'
    ? `Disable ${appLabel(current.app)}?`
    : `Remove ${current.deployment.host}?`
})
const confirmDescription = computed(() =>
  removingCurrentHost.value
    ? 'You are using this host right now. Removing it will cut off access to the portal through it.'
    : pending.value?.kind === 'disable'
    ? 'Nobody can open this app until it is enabled again. Its hosts and grants are kept.'
    : 'Requests for this host stop reaching the app, and the portal answers them as an unknown host.',
)
</script>

<template>
  <BaseStack :gap="4">
    <BaseCluster justify="between">
      <BaseHeading :level="2" size="lg">
        Apps
      </BaseHeading>
      <BaseButton v-if="canEdit" @click="createOpen = true">
        <BaseIcon name="plus" size="sm" />
        New app
      </BaseButton>
    </BaseCluster>
    <BaseText v-if="!canEdit" size="sm" tone="muted">
      Only platform administrators change apps and their hosts.
    </BaseText>

    <BaseText role="status" tone="success" size="sm" :class="notice ? undefined : 'sr-only'">
      {{ notice }}
    </BaseText>
    <BaseCluster v-if="uncertain" :gap="2">
      <BaseText role="alert" tone="warning" size="sm">
        {{ uncertain }}
      </BaseText>
      <BaseButton size="sm" variant="secondary" :loading="list.loading.value" @click="reloadList">
        Reload list
      </BaseButton>
    </BaseCluster>
    <BaseText v-if="actionError" role="alert" tone="danger" size="sm">
      {{ actionError }}
    </BaseText>

    <BaseBox as="div" :aria-busy="list.loading.value">
      <BaseStack v-if="list.loading.value" :gap="3">
        <BaseText class="sr-only">
          Loading apps
        </BaseText>
        <BaseSkeleton />
        <BaseSkeleton />
      </BaseStack>
      <FeaturesHomeLoadError
        v-else-if="list.error.value && list.items.value.length === 0"
        :error="list.error.value"
        subject="apps"
        forbidden-description="Your account is not allowed to see the apps of this initiative."
        @retry="list.reload()"
      />
      <BaseStack v-else :gap="4">
        <FeaturesAppsTable
          caption="Apps"
          :apps="list.items.value"
          :can-edit="canEdit"
          :busy-id="rowBusyId"
          @rename="openRename"
          @toggle="onToggle"
          @add-host="openAddHost"
          @remove-host="(app, deployment) => ask({ kind: 'remove-host', app, deployment })"
        />
        <BaseCluster v-if="list.error.value" :gap="2">
          <BaseText role="alert" size="sm" tone="danger">
            Could not load more apps.
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
      <FeaturesAppsCreateDialog
        v-model:open="createOpen"
        :initiative-id="initiativeId"
        @created="onCreated"
        @uncertain="onUncertain"
      />
      <FeaturesAppsRenameDialog v-model:open="renameOpen" :app="renameTarget" @saved="onRenamed" />
      <FeaturesAppsAddHostDialog v-model:open="hostOpen" :app="hostTarget" @added="onHostAdded" />
      <UiConfirmDialog
        v-model:open="confirmOpen"
        :title="confirmTitle"
        :description="confirmDescription"
        variant="danger"
        :confirm-label="pending?.kind === 'disable' ? 'Disable app' : 'Remove host'"
        :busy="confirmBusy"
        :error="confirmError"
        @confirm="confirm"
      />
    </template>
  </BaseStack>
</template>
