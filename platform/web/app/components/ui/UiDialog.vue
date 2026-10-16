<script setup lang="ts">
import { DialogClose, DialogContent, DialogDescription, DialogOverlay, DialogPortal, DialogRoot, DialogTitle } from 'reka-ui'

/**
 * Modal dialog on Reka UI's Dialog. Controlled with `v-model:open`; the body goes in the default slot.
 * Reka runtime behavior under the strict CSP (reka-ui 2.11.0), read from its source: the overlay (scroll lock) sets
 * `overflow`, `padding-right`, `margin-right` and `pointer-events` on `document.body` and a `--scrollbar-width`
 * property on `<html>`; the dismissable layer and the overlay bind `pointer-events` in a `:style`; Presence may set
 * `animation-fill-mode`; `aria-hidden` is toggled on the siblings of the portal. All of it goes through CSSOM
 * (`el.style.x = ...`, which `style-src 'self'` allows). No `<style>` element, no `setAttribute('style')` and no
 * focus-guard spans (those belong to Select, Menu, Popover and Combobox). The portal adds children to `<body>`.
 * `busy` blocks every way of closing it (Escape, outside click, the close button) while a request is in flight.
 * Focus: `initialFocus` (an object with `focus()`, e.g. a `BaseInput` template ref) receives focus on open instead
 * of the first tabbable element. On close focus returns to the element that opened the dialog, captured by
 * `useFocusReturn` (works when the click did not focus the button, as in Safari).
 */
const props = defineProps<{
  title: string
  description: string
  busy?: boolean
  initialFocus?: { focus: () => void } | null
}>()

const open = defineModel<boolean>('open', { required: true })
const focusReturn = useFocusReturn()

watch(open, (isOpen) => {
  if (isOpen) focusReturn.capture()
}, { flush: 'sync' })

function onOpenAutoFocus(event: Event) {
  if (!props.initialFocus) return
  event.preventDefault()
  props.initialFocus.focus()
}

function onDismiss(event: Event) {
  if (props.busy) event.preventDefault()
}
</script>

<template>
  <DialogRoot v-model:open="open">
    <DialogPortal>
      <DialogOverlay class="fixed inset-0 z-50 bg-text/50" />
      <DialogContent
        class="fixed inset-4 z-50 m-auto h-fit max-h-svh max-w-reading overflow-y-auto rounded-lg border border-border bg-surface p-6 shadow-md focus:outline-none"
        @escape-key-down="onDismiss"
        @pointer-down-outside="onDismiss"
        @interact-outside="onDismiss"
        @open-auto-focus="onOpenAutoFocus"
        @close-auto-focus="focusReturn.restore"
      >
        <BaseStack :gap="4">
          <BaseCluster justify="between" align="start" :wrap="false">
            <BaseStack :gap="1">
              <DialogTitle as-child>
                <BaseHeading :level="2" size="xl">
                  {{ title }}
                </BaseHeading>
              </DialogTitle>
              <DialogDescription as-child>
                <BaseText tone="muted" size="sm">
                  {{ description }}
                </BaseText>
              </DialogDescription>
            </BaseStack>
            <DialogClose
              :disabled="busy"
              aria-label="Close"
              class="cursor-pointer rounded-sm p-1 text-text-muted hover:text-text focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus disabled:cursor-not-allowed disabled:opacity-60"
            >
              <BaseIcon name="x" />
            </DialogClose>
          </BaseCluster>
          <slot />
        </BaseStack>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
