<script setup lang="ts">
import type { CreatedToken, CreateTokenBody, TokenScope } from '~/types/api'
import type { SelectOption } from '~/types/ui'

/**
 * Creates a token at `path` (`/me/tokens` or `/robots/{id}/tokens`) and shows its secret ONCE. Two states in one
 * dialog: the form, then the secret. The secret lives only in the local `secret` ref (never in a store, storage,
 * URL or the console) and is wiped when the dialog closes or unmounts; while it is shown the dialog is persistent
 * (no Escape or outside click), so it closes only with "Done" or the X. The 201 is not idempotent: after an ambiguous
 * failure the dialog closes and emits `uncertain` (the panel reloads the list and says the result is unknown);
 * definitive 4xx stay in the form. `offerAdmin` only hides the admin scope: the API decides
 * (422 `scope.not_allowed`). Fixed copy only.
 */
const props = defineProps<{
  path: string
  /** Offer the admin scope (the API still refuses it unless the owner administers something). */
  offerAdmin: boolean
  /** Whose token it is, for the description ("you" or "the robot ci"). */
  owner: string
  expiryOptions?: SelectOption[]
}>()
const emit = defineEmits<{ created: [], uncertain: [] }>()
const open = defineModel<boolean>('open', { required: true })

const api = useApi()
const name = ref('')
const scope = ref<TokenScope>('read')
const expiry = ref(EXPIRY_DEFAULT)
const errors = reactive({ name: '', scope: '', expires_in: '' })
const formError = ref('')
const submitting = ref(false)
const secret = ref('')
const copyNote = ref('')
const copyFailed = ref(false)

type Focusable = { focus: () => void }
const nameInput = useTemplateRef<Focusable>('nameInput')
const scopeInput = useTemplateRef<Focusable>('scopeInput')
const expiryInput = useTemplateRef<Focusable>('expiryInput')
const secretInput = useTemplateRef<Focusable>('secretInput')

const scopeOptions = computed<SelectOption[]>(() => SCOPE_OPTIONS.filter(o => o.value === 'read' || props.offerAdmin))

watch(open, (isOpen) => {
  secret.value = ''
  copyNote.value = ''
  copyFailed.value = false
  if (!isOpen) return
  name.value = ''
  scope.value = 'read'
  expiry.value = EXPIRY_DEFAULT
  errors.name = errors.scope = errors.expires_in = ''
  formError.value = ''
})
onBeforeUnmount(() => {
  secret.value = ''
})

function focusFirstInvalid() {
  if (errors.name) nameInput.value?.focus()
  else if (errors.scope) scopeInput.value?.focus()
  else if (errors.expires_in) expiryInput.value?.focus()
}

async function submit() {
  if (submitting.value) return
  errors.name = validateTokenName(name.value.trim()) ?? ''
  errors.scope = errors.expires_in = formError.value = ''
  if (errors.name) {
    focusFirstInvalid()
    return
  }
  const body: CreateTokenBody = { name: name.value.trim(), scope: scope.value }
  if (expiry.value !== EXPIRY_MAXIMUM) body.expires_in = Number(expiry.value)
  submitting.value = true
  try {
    const created = await api.post<CreatedToken>(props.path, body)
    secret.value = created.secret
    emit('created')
    await nextTick()
    secretInput.value?.focus()
  }
  catch (e) {
    if (isAmbiguousFailure(e)) {
      open.value = false
      emit('uncertain')
      return
    }
    const failure = describeMutationError(e, {
      conflict: TOKEN_LIMIT_COPY,
      notFound: 'The owner no longer exists.',
      fields: ['name', 'scope', 'expires_in'],
      fieldCopy: TOKEN_FIELD_COPY,
    })
    errors.name = failure.fields.name ?? ''
    errors.scope = failure.fields.scope ?? ''
    errors.expires_in = failure.fields.expires_in ?? ''
    formError.value = failure.message
    focusFirstInvalid()
  }
  finally {
    submitting.value = false
  }
}

async function copy() {
  copyNote.value = ''
  copyFailed.value = false
  try {
    await navigator.clipboard.writeText(secret.value)
    copyNote.value = 'Secret copied to the clipboard.'
  }
  catch {
    copyNote.value = 'Could not copy. Select the secret and copy it by hand.'
    copyFailed.value = true
  }
}
</script>

<template>
  <UiDialog
    v-model:open="open"
    :title="secret ? 'Token created' : 'New token'"
    :description="secret
      ? 'Copy the secret now. You will not see it again.'
      : `Create an API token for ${owner}. It never has more permissions than its owner, and the scope can only lower them.`"
    :busy="submitting"
    :persistent="!!secret"
    :initial-focus="nameInput"
  >
    <BaseStack v-if="secret" :gap="4">
      <BaseField
        v-slot="field"
        label="Secret"
        hint="This is the only time the secret is shown. If you lose it, revoke the token and create another. Treat it like a password."
      >
        <BaseInput
          ref="secretInput"
          v-bind="field"
          :model-value="secret"
          readonly
          autocomplete="off"
          spellcheck="false"
          class="font-mono"
        />
      </BaseField>
      <BaseText
        :role="copyFailed ? 'alert' : 'status'"
        size="sm"
        :tone="copyFailed ? 'danger' : 'success'"
        :class="copyNote ? undefined : 'sr-only'"
      >
        {{ copyNote }}
      </BaseText>
      <BaseCluster justify="end">
        <BaseButton variant="secondary" @click="copy">
          Copy secret
        </BaseButton>
        <BaseButton @click="open = false">
          Done
        </BaseButton>
      </BaseCluster>
    </BaseStack>
    <form v-else novalidate @submit.prevent="submit">
      <BaseStack :gap="4">
        <BaseField v-slot="field" label="Name" required :hint="`What it is for. Up to ${TOKEN_NAME_MAX} characters.`" :error="errors.name">
          <BaseInput ref="nameInput" v-bind="field" v-model="name" autocomplete="off" />
        </BaseField>
        <BaseField
          v-slot="field"
          label="Scope"
          required
          :hint="offerAdmin ? undefined : 'Only read tokens are offered: the admin scope needs a platform admin or a manager.'"
          :error="errors.scope"
        >
          <BaseSelect ref="scopeInput" v-bind="field" v-model="scope" :options="scopeOptions" />
        </BaseField>
        <BaseField v-slot="field" label="Expires" required :error="errors.expires_in">
          <BaseSelect ref="expiryInput" v-bind="field" v-model="expiry" :options="expiryOptions ?? EXPIRY_OPTIONS" />
        </BaseField>
        <BaseText v-if="formError" role="alert" tone="danger" size="sm">
          {{ formError }}
        </BaseText>
        <BaseCluster justify="end">
          <BaseButton variant="secondary" :disabled="submitting" @click="open = false">
            Cancel
          </BaseButton>
          <BaseButton type="submit" :loading="submitting">
            Create token
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </form>
  </UiDialog>
</template>
