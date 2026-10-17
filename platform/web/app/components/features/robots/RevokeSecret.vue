<script setup lang="ts">
/**
 * Revokes a leaked token from its secret (platform admins; `POST /tokens/revoke`). The endpoint answers 204 for any
 * well-formed value, found or not, so this form never confirms that a token existed: the result is visible in the
 * token lists and the audit log. 422 = malformed. The pasted value is never echoed back and the field is cleared
 * after every submit; nothing is stored or logged. The field is plain text on purpose (no type=password): a password
 * manager would offer to save a leaked secret.
 */
const token = ref('')
const error = ref('')
const notice = ref('')
const submitting = ref(false)
const input = useTemplateRef<{ focus: () => void }>('input')

async function submit() {
  if (submitting.value) return
  const value = token.value.trim()
  error.value = notice.value = ''
  if (!value) {
    error.value = 'Paste the leaked token.'
    input.value?.focus()
    return
  }
  submitting.value = true
  try {
    await apiSend('POST', '/tokens/revoke', { token: value })
    notice.value = 'Request accepted. If the value was a live token, it is now revoked. This form never confirms whether a token existed: check the token lists or the audit log.'
  }
  catch (e) {
    const f = describeMutationError(e, {
      conflict: 'The request could not be completed.',
      fields: ['token'],
      fieldCopy: {
        'token.required': 'Paste the leaked token.',
        'token.invalid': 'This is not a portal token. A token starts with ptk_ and has 55 characters.',
      },
    })
    // The field is cleared below, so a retry needs the value again.
    error.value = f.fields.token ?? (f.retryable ? `${f.message} Paste the token again.` : f.message)
    input.value?.focus()
  }
  finally {
    token.value = ''
    submitting.value = false
  }
}
</script>

<template>
  <BaseCard as="section" aria-labelledby="revoke-secret-title">
    <form novalidate @submit.prevent="submit">
      <BaseStack :gap="4">
        <BaseHeading id="revoke-secret-title" :level="2" size="lg">
          Revoke a leaked token
        </BaseHeading>
        <BaseText size="sm" tone="muted">
          Paste a token found in a leak report. It is revoked if it is live; the portal never says whether it existed. The value is not stored and is cleared after sending.
        </BaseText>
        <BaseField v-slot="field" label="Leaked token" :error="error">
          <BaseInput ref="input" v-bind="field" v-model="token" autocomplete="off" autocapitalize="none" spellcheck="false" />
        </BaseField>
        <BaseText role="status" tone="success" size="sm" :class="notice ? undefined : 'sr-only'">
          {{ notice }}
        </BaseText>
        <BaseCluster>
          <BaseButton type="submit" variant="danger" :loading="submitting">
            Revoke token
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </form>
  </BaseCard>
</template>
