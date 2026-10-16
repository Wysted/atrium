<script setup lang="ts">
import type { AdminUser, AdminUserList, PatchUserBody, RevokeSessionsResult } from '~/types/api'

/**
 * Users screen (platform admins only; the page does not mount it for anyone else and the API enforces). Lookup by
 * exact email (case-insensitive): `GET /users?email=` answers a list because the email is not unique (at most 50,
 * no paging). Per user: Disable/Enable (PATCH status; disabling also ends their sessions) and Revoke sessions,
 * each behind a confirmation. Lockout guards: disabling yourself and disabling the last active platform admin are
 * 409s with their own fixed copy; "Disable" is also hidden for your own row. Revoking your own sessions warns
 * first and, on success, reloads the page so ForwardAuth sends you to sign in. Fixed copy only.
 */
const api = useApi()
const access = useAdminAccess()
const selfId = computed(() => access.me.value?.user.id ?? null)

const email = ref('')
const emailError = ref('')
const searching = ref(false)
const searchError = ref('')
const searched = ref(false)
const users = shallowRef<AdminUser[]>([])
const searchedEmail = ref('')
const announcement = ref('')
const emailInput = useTemplateRef<{ focus: () => void }>('emailInput')
let generation = 0

async function search() {
  const value = email.value.trim()
  emailError.value = validateEmail(value) ?? ''
  searchError.value = ''
  if (emailError.value) {
    emailInput.value?.focus()
    return
  }
  const mine = ++generation
  searching.value = true
  notice.value = ''
  users.value = []
  searched.value = false
  announcement.value = ''
  try {
    const result = await api.get<AdminUserList>(`/users?email=${encodeURIComponent(value)}`)
    if (mine !== generation) return
    users.value = result.users
    searchedEmail.value = value
    searched.value = true
    announcement.value = result.users.length === 1 ? '1 user found' : `${result.users.length} users found`
  }
  catch (e) {
    if (mine !== generation) return
    const failure = describeMutationError(e, { conflict: 'The search could not be completed.', fields: ['email'], fieldCopy: EMAIL_FIELD_COPY })
    emailError.value = failure.fields.email ?? ''
    searchError.value = emailError.value ? '' : failure.message
    if (emailError.value) emailInput.value?.focus()
  }
  finally {
    if (mine === generation) searching.value = false
  }
}

const notice = ref('')
const busyId = ref<string | null>(null)

// Disable / enable.
const statusTarget = ref<AdminUser | null>(null)
const statusOpen = computed({
  get: () => statusTarget.value !== null,
  set: (value: boolean) => {
    if (!value) statusTarget.value = null
  },
})
const statusBusy = ref(false)
const statusError = ref('')
const disabling = computed(() => statusTarget.value?.status === 'active')

function askStatus(user: AdminUser) {
  notice.value = ''
  statusError.value = ''
  statusTarget.value = user
}

async function confirmStatus() {
  const user = statusTarget.value
  if (!user || statusBusy.value) return
  const body: PatchUserBody = { status: user.status === 'active' ? 'disabled' : 'active' }
  statusBusy.value = true
  busyId.value = user.id
  statusError.value = ''
  try {
    const updated = await api.patch<AdminUser>(`/users/${encodeURIComponent(user.id)}`, body)
    users.value = users.value.map(u => (u.id === updated.id ? updated : u))
    statusTarget.value = null
    notice.value = body.status === 'disabled' ? 'User disabled and signed out.' : 'User enabled.'
  }
  catch (e) {
    statusError.value = describeMutationError(e, {
      // Go checks the self guard first, so a 409 on the caller's own row is that one, otherwise the last-admin guard.
      conflict: user.id === selfId.value ? SELF_DISABLE_COPY : LAST_ADMIN_COPY,
      notFound: 'This user no longer exists. Search again.',
    }).message
  }
  finally {
    statusBusy.value = false
    busyId.value = null
  }
}

// Revoke sessions.
const revokeTarget = ref<AdminUser | null>(null)
const revokeOpen = computed({
  get: () => revokeTarget.value !== null,
  set: (value: boolean) => {
    if (!value) revokeTarget.value = null
  },
})
const revokeBusy = ref(false)
const revokeError = ref('')
const revokingSelf = computed(() => revokeTarget.value !== null && revokeTarget.value.id === selfId.value)

function askRevoke(user: AdminUser) {
  notice.value = ''
  revokeError.value = ''
  revokeTarget.value = user
}

async function confirmRevoke() {
  const user = revokeTarget.value
  if (!user || revokeBusy.value) return
  revokeBusy.value = true
  busyId.value = user.id
  revokeError.value = ''
  try {
    const result = await apiSend<RevokeSessionsResult>('DELETE', `/users/${encodeURIComponent(user.id)}/sessions`)
    revokeTarget.value = null
    if (user.id === selfId.value) {
      // Our own session is gone: reload so ForwardAuth answers with the sign-in redirect.
      window.location.reload()
      return
    }
    const n = result.revoked
    notice.value = n === 1 ? '1 session revoked.' : `${n} sessions revoked.`
  }
  catch (e) {
    revokeError.value = describeMutationError(e, {
      conflict: 'The sessions could not be revoked. Reload the page and try again.',
      notFound: 'This user no longer exists. Search again.',
    }).message
  }
  finally {
    revokeBusy.value = false
    busyId.value = null
  }
}
</script>

<template>
  <BaseStack :gap="6">
    <BasePageHeader title="Users" description="Find a user by their exact email to disable or enable the account, or to end their sessions." />

    <form novalidate @submit.prevent="search">
      <BaseCluster align="start" :gap="3">
        <BaseField v-slot="field" label="Email" :error="emailError">
          <BaseInput ref="emailInput" v-bind="field" v-model="email" type="email" autocomplete="off" spellcheck="false" />
        </BaseField>
        <BaseButton type="submit" :loading="searching">
          <BaseIcon name="search" size="sm" />
          Search
        </BaseButton>
      </BaseCluster>
    </form>

    <BaseText role="status" tone="success" size="sm" :class="notice ? undefined : 'sr-only'">
      {{ notice }}
    </BaseText>
    <BaseText v-if="searchError" role="alert" tone="danger" size="sm">
      {{ searchError }}
    </BaseText>

    <BaseText role="status" aria-live="polite" class="sr-only">
      {{ announcement }}
    </BaseText>

    <BaseBox v-if="searched" as="div" :aria-busy="searching">
      <BaseStack :gap="2">
        <BaseText size="sm" tone="muted">
          Results for {{ searchedEmail }}
        </BaseText>
        <FeaturesUsersTable
          caption="Users"
          :users="users"
          :self-id="selfId"
          :busy-id="busyId"
          @set-status="askStatus"
          @revoke-sessions="askRevoke"
        />
      </BaseStack>
    </BaseBox>

    <UiConfirmDialog
      v-model:open="statusOpen"
      :title="`${disabling ? 'Disable' : 'Enable'} ${statusTarget?.email ?? 'user'}?`"
      :description="disabling
        ? 'They are signed out now and cannot sign in or open any app until the account is enabled again. Their grants and group memberships are kept.'
        : 'They can sign in again and regain the access their grants and groups give.'"
      :confirm-label="disabling ? 'Disable' : 'Enable'"
      :variant="disabling ? 'danger' : 'primary'"
      :busy="statusBusy"
      :error="statusError"
      @confirm="confirmStatus"
    />
    <UiConfirmDialog
      v-model:open="revokeOpen"
      :title="`Revoke the sessions of ${revokeTarget?.email ?? 'user'}?`"
      :description="revokingSelf
        ? 'This is your own account: you will be signed out of the portal and every app, and will need to sign in again.'
        : 'They are signed out of the portal and every app, and must sign in again. The account stays enabled.'"
      confirm-label="Revoke sessions"
      :busy="revokeBusy"
      :error="revokeError"
      @confirm="confirmRevoke"
    />
  </BaseStack>
</template>
