<script setup lang="ts">
import type { Grant, GrantList } from '~/types/api'
import { ApiError } from '~/composables/useApi'

/**
 * Grants screen of an initiative. With `canEdit` (platform admins and managers of this initiative; the API
 * enforces) it creates grants (user by email or group, role, optional expiry) and revokes them behind a
 * confirmation; 409 on revoke = last effective manager. Everyone else reads the list.
 * Keyset pagination with "Load more". Expired grants are listed with a badge. Fixed copy only.
 */
const props = defineProps<{
  initiativeId: string
  canEdit: boolean
}>()

const api = useApi()
const access = useAdminAccess()
const list = usePagedList<GrantList, Grant>(
  `/initiatives/${encodeURIComponent(props.initiativeId)}/grants`,
  page => page.grants,
)

const createOpen = ref(false)
const revoking = ref<Grant | null>(null)
const revokeOpen = computed({
  get: () => revoking.value !== null,
  set: (value: boolean) => {
    if (!value) revoking.value = null
  },
})
const revokeBusy = ref(false)
const revokeError = ref('')
const notice = ref('')

function askRevoke(grant: Grant) {
  notice.value = ''
  revokeError.value = ''
  revoking.value = grant
}

async function confirmRevoke() {
  const grant = revoking.value
  if (!grant || revokeBusy.value) return
  revokeBusy.value = true
  revokeError.value = ''
  try {
    await api.del(`/grants/${encodeURIComponent(grant.id)}`)
    list.items.value = list.items.value.filter(g => g.id !== grant.id)
    revoking.value = null
    notice.value = 'Grant revoked.'
    // The revoked grant may have been the caller's own: refresh what they may administer.
    void access.revalidate()
  }
  catch (e) {
    if (e instanceof ApiError && e.kind === 'not_found') {
      // Someone else already revoked it: close the dialog and show the real list.
      revoking.value = null
      notice.value = 'This grant no longer exists.'
      void list.reload()
    }
    else {
      revokeError.value = describeMutationError(e, { conflict: LAST_MANAGER_COPY }).message
    }
  }
  finally {
    revokeBusy.value = false
  }
}

function onCreated() {
  notice.value = 'Access granted.'
  void list.reload()
}

const subjectLabel = computed(() => {
  const grant = revoking.value
  if (!grant) return ''
  if (grant.user) return grant.user.email
  return grant.group ? `group ${grant.group.key}` : ''
})
</script>

<template>
  <BaseStack :gap="4">
    <BaseCluster justify="between">
      <BaseHeading :level="2" size="lg">
        Grants
      </BaseHeading>
      <BaseButton v-if="canEdit" @click="createOpen = true">
        <BaseIcon name="plus" size="sm" />
        Grant access
      </BaseButton>
    </BaseCluster>
    <BaseText v-if="!canEdit" size="sm" tone="muted">
      Only platform administrators and managers of this initiative change grants.
    </BaseText>

    <BaseText role="status" tone="success" size="sm" :class="notice ? undefined : 'sr-only'">
      {{ notice }}
    </BaseText>

    <BaseBox as="div" :aria-busy="list.loading.value">
      <BaseStack v-if="list.loading.value" :gap="3">
        <BaseText class="sr-only">
          Loading grants
        </BaseText>
        <BaseSkeleton />
        <BaseSkeleton />
        <BaseSkeleton />
      </BaseStack>
      <FeaturesHomeLoadError
        v-else-if="list.error.value && list.items.value.length === 0"
        :error="list.error.value"
        subject="grants"
        forbidden-description="Your account is not allowed to see the grants of this initiative."
        @retry="list.reload()"
      />
      <BaseStack v-else :gap="4">
        <FeaturesGrantsTable
          caption="Grants"
          :grants="list.items.value"
          :can-revoke="canEdit"
          @revoke="askRevoke"
        />
        <BaseCluster v-if="list.error.value" :gap="2">
          <BaseText role="alert" size="sm" tone="danger">
            Could not load more grants.
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
      <FeaturesGrantsCreateDialog v-model:open="createOpen" :initiative-id="initiativeId" @created="onCreated" />
      <UiConfirmDialog
        v-model:open="revokeOpen"
        :title="`Revoke ${revoking?.role.key ?? 'role'} from ${subjectLabel}?`"
        description="They lose the access this grant gives. Other grants they hold, directly or through a group, stay."
        confirm-label="Revoke"
        :busy="revokeBusy"
        :error="revokeError"
        @confirm="confirmRevoke"
      />
    </template>
  </BaseStack>
</template>
