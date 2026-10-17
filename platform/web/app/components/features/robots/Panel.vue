<script setup lang="ts">
import type { PatchRobotBody, Robot, RobotList } from '~/types/api'
import type { SelectOption } from '~/types/ui'
import { ApiError } from '~/composables/useApi'

/**
 * Robots screen (platform admins only; the page does not mount it for anyone else and the API enforces).
 * Keyset list filterable by status (`?status=`; changing it reloads from the first page), create / rename / change the owner (one form dialog) and disable / enable behind a confirmation.
 * Disabling a robot revokes all its tokens at once and enabling it does not bring them back: the dialog says so.
 * Fixed copy only.
 */
const api = useApi()
const access = useAdminAccess()
const selfId = computed(() => access.me.value?.user.id ?? null)
const STATUS_OPTIONS: SelectOption[] = [
  { value: '', label: 'All' },
  { value: 'active', label: 'Active' },
  { value: 'disabled', label: 'Disabled' },
]
const statusFilter = ref('')
// A cursor is only valid with the filter it came from: reload() restarts at the first page.
const list = usePagedList<RobotList, Robot>(
  () => (statusFilter.value ? `/robots?status=${statusFilter.value}` : '/robots'),
  page => page.robots,
)
watch(statusFilter, () => void list.reload())

const notice = ref('')
const uncertain = ref('')
const busyId = ref<string | null>(null)
const formOpen = ref(false)
const formRobot = ref<Robot | null>(null)

function askCreate() {
  notice.value = ''
  uncertain.value = ''
  formRobot.value = null
  formOpen.value = true
}
function askEdit(robot: Robot) {
  notice.value = ''
  formRobot.value = robot
  formOpen.value = true
}
// Creating is not idempotent: after an ambiguous failure show the real list and say so.
function onUncertain() {
  notice.value = ''
  uncertain.value = 'The result is unknown. Check the list before creating another robot.'
  void list.reload(true)
}
function onSaved(robot: Robot) {
  uncertain.value = ''
  notice.value = formRobot.value ? 'Robot saved.' : 'Robot created.'
  void list.reload(true).then((ok) => {
    // Keep the saved row current only when the quiet reload failed and the row belongs to the current filter.
    const matches = !statusFilter.value || robot.status === statusFilter.value
    if (!ok && matches && !list.items.value.some(r => r.id === robot.id)) list.items.value = [...list.items.value, robot]
  })
}

const statusTarget = ref<Robot | null>(null)
const statusOpen = computed({
  get: () => statusTarget.value !== null,
  set: (value: boolean) => {
    if (!value) statusTarget.value = null
  },
})
const statusBusy = ref(false)
const statusError = ref('')
const disabling = computed(() => statusTarget.value?.status === 'active')

function askStatus(robot: Robot) {
  notice.value = ''
  statusError.value = ''
  statusTarget.value = robot
}

async function confirmStatus() {
  const robot = statusTarget.value
  if (!robot || statusBusy.value) return
  const body: PatchRobotBody = { status: robot.status === 'active' ? 'disabled' : 'active' }
  statusBusy.value = true
  busyId.value = robot.id
  statusError.value = ''
  try {
    const updated = await api.patch<Robot>(`/robots/${encodeURIComponent(robot.id)}`, body)
    // Under a status filter the row no longer matches after the change: it leaves the list.
    list.items.value = statusFilter.value && updated.status !== statusFilter.value
      ? list.items.value.filter(r => r.id !== updated.id)
      : list.items.value.map(r => (r.id === updated.id ? updated : r))
    statusTarget.value = null
    notice.value = body.status === 'disabled' ? 'Robot disabled and its tokens revoked.' : 'Robot enabled.'
  }
  catch (e) {
    if (e instanceof ApiError && e.kind === 'not_found') {
      statusTarget.value = null
      notice.value = 'This robot no longer exists.'
      void list.reload(true)
    }
    else {
      statusError.value = describeMutationError(e, { conflict: 'The status could not be changed. Reload the page and try again.' }).message
    }
  }
  finally {
    statusBusy.value = false
    busyId.value = null
  }
}
</script>

<template>
  <BaseStack :gap="6">
    <BaseStack :gap="4">
      <BasePageHeader title="Robots" description="Robots are identities for automation. They get access through direct grants and call the API with tokens.">
        <BaseButton @click="askCreate">
          <BaseIcon name="plus" size="sm" />
          New robot
        </BaseButton>
      </BasePageHeader>

      <BaseText role="status" tone="success" size="sm" :class="notice ? undefined : 'sr-only'">
        {{ notice }}
      </BaseText>

      <BaseText v-if="uncertain" role="alert" tone="warning" size="sm">
        {{ uncertain }}
      </BaseText>

      <BaseField v-slot="field" label="Status">
        <BaseSelect v-bind="field" v-model="statusFilter" :options="STATUS_OPTIONS" />
      </BaseField>

      <BaseBox as="div" :aria-busy="list.loading.value">
        <BaseStack v-if="list.loading.value" :gap="3">
          <BaseText class="sr-only">
            Loading robots
          </BaseText>
          <BaseSkeleton />
          <BaseSkeleton />
        </BaseStack>
        <FeaturesHomeLoadError
          v-else-if="list.error.value && list.items.value.length === 0"
          :error="list.error.value"
          subject="robots"
          forbidden-description="Your account is not allowed to manage robots."
          @retry="list.reload()"
        />
        <BaseStack v-else :gap="4">
          <FeaturesRobotsTable
            caption="Robots"
            :robots="list.items.value"
            :self-id="selfId"
            :busy-id="busyId"
            :empty-text="statusFilter ? `No ${statusFilter} robots.` : undefined"
            @edit="askEdit"
            @set-status="askStatus"
          />
          <BaseCluster v-if="list.error.value" :gap="2">
            <BaseText role="alert" size="sm" tone="danger">
              Could not load more robots.
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
    </BaseStack>

    <FeaturesRobotsRevokeSecret />

    <FeaturesRobotsFormDialog v-model:open="formOpen" :robot="formRobot" @saved="onSaved" @uncertain="onUncertain" />
    <UiConfirmDialog
      v-model:open="statusOpen"
      :title="`${disabling ? 'Disable' : 'Enable'} robot ${statusTarget?.name ?? ''}?`"
      :description="disabling
        ? 'All its tokens are revoked now and anything using them stops working. Enabling the robot later does not bring them back: you create new tokens. Its grants are kept.'
        : 'The robot can be used again, but its old tokens stay revoked: create new ones.'"
      :confirm-label="disabling ? 'Disable' : 'Enable'"
      :variant="disabling ? 'danger' : 'primary'"
      :busy="statusBusy"
      :error="statusError"
      @confirm="confirmStatus"
    />
  </BaseStack>
</template>
