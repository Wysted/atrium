<script setup lang="ts">
import {
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogOverlay,
  AlertDialogPortal,
  AlertDialogRoot,
  AlertDialogTitle,
} from 'reka-ui'

/**
 * Confirmation for a destructive or hard-to-undo action, on Reka UI's AlertDialog (`role="alertdialog"`):
 * it does not close on an outside click and focuses "Cancel" first. Controlled with `v-model:open`.
 * It does NOT close itself on confirm: the parent runs the request (`busy` while it runs), shows a failure through
 * `error`, and sets `open` to false on success. While `busy` Escape and Cancel are blocked.
 * Style behavior under the strict CSP is the one of `UiDialog` (CSSOM only, no `<style>`); `AlertDialogContent`
 * wraps `DialogContent`, and `AlertDialogCancel` is a plain `<button>` that adds no styles.
 */
const props = withDefaults(defineProps<{
  title: string
  description: string
  confirmLabel: string
  cancelLabel?: string
  variant?: 'primary' | 'danger'
  busy?: boolean
  /** Fixed message of the last failed attempt, shown inside the dialog (`role="alert"`). */
  error?: string
}>(), {
  cancelLabel: 'Cancel',
  variant: 'danger',
  busy: false,
  error: undefined,
})

const emit = defineEmits<{ confirm: [] }>()
const open = defineModel<boolean>('open', { required: true })
const focusReturn = useFocusReturn()

// Sync flush: the trigger must be read before Reka moves focus into the dialog.
watch(open, (isOpen) => {
  if (isOpen) focusReturn.capture()
}, { flush: 'sync' })

function onEscape(event: Event) {
  if (props.busy) event.preventDefault()
}
</script>

<template>
  <AlertDialogRoot v-model:open="open">
    <AlertDialogPortal>
      <AlertDialogOverlay class="fixed inset-0 z-50 bg-text/50" />
      <AlertDialogContent
        class="fixed inset-4 z-50 m-auto h-fit max-h-svh max-w-reading overflow-y-auto rounded-lg border border-border bg-surface p-6 shadow-md focus:outline-none"
        @escape-key-down="onEscape"
        @close-auto-focus="focusReturn.restore"
      >
        <BaseStack :gap="4">
          <BaseStack :gap="1">
            <AlertDialogTitle as-child>
              <BaseHeading :level="2" size="xl">
                {{ title }}
              </BaseHeading>
            </AlertDialogTitle>
            <AlertDialogDescription as-child>
              <BaseText tone="muted" size="sm">
                {{ description }}
              </BaseText>
            </AlertDialogDescription>
          </BaseStack>
          <slot />
          <BaseText v-if="error" role="alert" tone="danger" size="sm">
            {{ error }}
          </BaseText>
          <BaseCluster justify="end">
            <AlertDialogCancel as-child :disabled="busy">
              <BaseButton variant="secondary" :disabled="busy">
                {{ cancelLabel }}
              </BaseButton>
            </AlertDialogCancel>
            <BaseButton :variant="variant" :loading="busy" @click="emit('confirm')">
              {{ confirmLabel }}
            </BaseButton>
          </BaseCluster>
        </BaseStack>
      </AlertDialogContent>
    </AlertDialogPortal>
  </AlertDialogRoot>
</template>
