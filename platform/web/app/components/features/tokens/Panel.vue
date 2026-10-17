<script setup lang="ts">
import type { ApiToken, TokenList } from '~/types/api'
import type { HeadingLevel, SelectOption } from '~/types/ui'
import { ApiError } from '~/composables/useApi'

/**
 * Tokens of one owner: keyset list (expired and revoked ones stay visible for 30 days), "New token" (the secret is
 * shown once by the dialog) and revoke behind a confirmation. Used for the caller's own tokens (`/me/tokens`,
 * revoked at `DELETE /me/tokens/{id}`) and for a robot's (`/robots/{id}/tokens`, revoked by a platform admin at
 * `DELETE /tokens/{id}`). `canCreate` false hides "New token" (e.g. a disabled robot). The API enforces everything.
 */
const props = defineProps<{
  /** Collection path: list (GET) and creation (POST). */
  path: string
  /** Base path of the revocation: `DELETE {revokeBase}/{id}`. */
  revokeBase: string
  offerAdmin: boolean
  canCreate: boolean
  /** "you" or "the robot ci". */
  owner: string
  expiryOptions?: SelectOption[]
  /** Title of the list (default "Tokens"). */
  heading?: string
  headingLevel?: HeadingLevel
}>()

const api = useApi()
const list = usePagedList<TokenList, ApiToken>(props.path, page => page.tokens)

const createOpen = ref(false)
const notice = ref('')
const uncertain = ref('')
const revoking = ref<ApiToken | null>(null)
const revokeOpen = computed({
  get: () => revoking.value !== null,
  set: (value: boolean) => {
    if (!value) revoking.value = null
  },
})
const revokeBusy = ref(false)
const revokeError = ref('')

function askRevoke(token: ApiToken) {
  notice.value = ''
  uncertain.value = ''
  revokeError.value = ''
  revoking.value = token
}

async function confirmRevoke() {
  const token = revoking.value
  if (!token || revokeBusy.value) return
  revokeBusy.value = true
  revokeError.value = ''
  try {
    await api.del(`${props.revokeBase}/${encodeURIComponent(token.id)}`)
    revoking.value = null
    notice.value = 'Token revoked.'
    void list.reload(true)
  }
  catch (e) {
    if (e instanceof ApiError && e.kind === 'not_found') {
      revoking.value = null
      notice.value = 'This token no longer exists.'
      void list.reload(true)
    }
    else {
      revokeError.value = describeMutationError(e, { conflict: 'The token could not be revoked. Reload the page and try again.' }).message
    }
  }
  finally {
    revokeBusy.value = false
  }
}

function onCreated() {
  uncertain.value = ''
  notice.value = 'Token created.'
  void list.reload(true)
}

// The creation is not idempotent: after an ambiguous failure show the real list and say so.
function onUncertain() {
  notice.value = ''
  uncertain.value = TOKEN_UNCERTAIN_COPY
  void list.reload(true)
}
</script>

<template>
  <BaseStack :gap="4">
    <BaseCluster justify="between">
      <BaseHeading :level="headingLevel ?? 2" size="lg">
        {{ heading ?? 'Tokens' }}
      </BaseHeading>
      <BaseButton v-if="canCreate" @click="createOpen = true">
        <BaseIcon name="plus" size="sm" />
        New token
      </BaseButton>
    </BaseCluster>

    <BaseText role="status" tone="success" size="sm" :class="notice ? undefined : 'sr-only'">
      {{ notice }}
    </BaseText>

    <BaseText v-if="uncertain" role="alert" tone="warning" size="sm">
      {{ uncertain }}
    </BaseText>

    <BaseBox as="div" :aria-busy="list.loading.value">
      <BaseStack v-if="list.loading.value" :gap="3">
        <BaseText class="sr-only">
          Loading tokens
        </BaseText>
        <BaseSkeleton />
        <BaseSkeleton />
      </BaseStack>
      <FeaturesHomeLoadError
        v-else-if="list.error.value && list.items.value.length === 0"
        :error="list.error.value"
        subject="tokens"
        forbidden-description="Your account is not allowed to see these tokens."
        @retry="list.reload()"
      />
      <BaseStack v-else :gap="4">
        <FeaturesTokensTable :caption="heading ?? 'Tokens'" :tokens="list.items.value" @revoke="askRevoke" />
        <BaseCluster v-if="list.error.value" :gap="2">
          <BaseText role="alert" size="sm" tone="danger">
            Could not load more tokens.
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

    <FeaturesTokensCreateDialog
      v-if="canCreate"
      v-model:open="createOpen"
      :path="path"
      :offer-admin="offerAdmin"
      :owner="owner"
      :expiry-options="expiryOptions"
      @created="onCreated"
      @uncertain="onUncertain"
    />
    <UiConfirmDialog
      v-model:open="revokeOpen"
      :title="`Revoke token ${revoking?.name ?? ''}?`"
      :description="`Anything using this token stops working at its next request. This cannot be undone${canCreate ? ': create a new token to replace it' : ''}.`"
      confirm-label="Revoke token"
      :busy="revokeBusy"
      :error="revokeError"
      @confirm="confirmRevoke"
    />
  </BaseStack>
</template>
