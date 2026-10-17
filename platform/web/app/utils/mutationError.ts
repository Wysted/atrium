import { ApiError } from '~/composables/useApi'

/**
 * What a form or a confirm dialog shows when a mutation fails. Built only from fixed English copy chosen by
 * status and by validation code: server-provided `detail` text is never displayed.
 */
export interface MutationFailure {
  /** Form-level message (a banner above the actions). Always set. */
  message: string
  /** Per-field messages keyed by the API field name, only for the fields the caller declared. */
  fields: Record<string, string>
  /** True when the same request may simply be sent again (503, network). */
  retryable: boolean
}

export interface MutationCopy {
  /** Message for a 409: it depends on the action (duplicate key, last manager, platform initiative...). */
  conflict: string
  /** Message for a 404 (default: the item no longer exists). */
  notFound?: string
  /** API field names the form renders an error for. Errors on other fields fold into the form-level message. */
  fields?: readonly string[]
  /** Overrides of the generic copy, keyed `<field>.<code>`. */
  fieldCopy?: Readonly<Record<string, string>>
}

/** Copy for each validation code of a 422 `errors[]` entry (portal.Code*). Unknown codes fall back to `invalid`. */
const CODE_COPY: Readonly<Record<string, string>> = {
  required: 'This field is required.',
  invalid: 'This value is not valid.',
  must_be_in_the_future: 'Choose a date in the future.',
  no_unique_active_user: 'No single active user matches this email.',
  unknown: 'This value is not recognized.',
  exactly_one_required: 'Provide exactly one of the options.',
  reserved: 'This value is reserved.',
  at_least_one_required: 'Provide at least one value.',
  not_allowed: 'This value is not allowed.',
  too_large: 'This value is too large.',
}

const GENERIC_INVALID = 'Check the values and try again.'
const REQUEST_REJECTED = 'The request was not accepted. Reload the page and try again.'

function retryMessage(error: ApiError): string {
  const seconds = error.retryAfter
  return seconds && seconds > 0
    ? `The portal is busy. Try again in ${seconds} ${seconds === 1 ? 'second' : 'seconds'}.`
    : 'The portal is busy. Try again in a moment.'
}

/** Maps any failure of a mutation to fixed copy. */
export function describeMutationError(e: unknown, copy: MutationCopy): MutationFailure {
  const error = e instanceof ApiError ? e : new ApiError(0, null)
  const fail = (message: string, retryable = false, fields: Record<string, string> = {}): MutationFailure =>
    ({ message, fields, retryable })

  switch (error.kind) {
    case 'unauthorized':
      return fail(error.reloading
        ? 'Your session ended. Redirecting to sign in.'
        : 'Your session has ended. Reload the page to sign in again.')
    case 'forbidden':
      return fail('You do not have permission to do this.')
    case 'not_found':
      return fail(copy.notFound ?? 'This item no longer exists. Reload the page.')
    case 'conflict':
      return fail(copy.conflict)
    case 'unavailable':
      return fail(retryMessage(error), true)
    case 'network':
      return fail('Could not reach the portal. Check your connection and try again.', true)
    case 'server':
      return fail('Something went wrong on our side. Try again.', true)
    case 'invalid':
      return validationFailure(error, copy)
    default:
      return fail('The request could not be completed. Try again.')
  }
}

function validationFailure(error: ApiError, copy: MutationCopy): MutationFailure {
  // Only a 422 carries `errors[]`; a 400/413/415 means the client sent something the API never documents.
  const problems = error.status === 422 ? (error.problem?.errors ?? []) : []
  if (problems.length === 0) {
    return { message: error.status === 422 ? GENERIC_INVALID : REQUEST_REJECTED, fields: {}, retryable: false }
  }
  const known = new Set(copy.fields ?? [])
  const fields: Record<string, string> = {}
  let unplaced = false
  for (const { field, code } of problems) {
    if (!known.has(field)) {
      unplaced = true
      continue
    }
    // First error per field wins.
    fields[field] ??= copy.fieldCopy?.[`${field}.${code}`] ?? CODE_COPY[code] ?? CODE_COPY.invalid!
  }
  const placed = Object.keys(fields).length > 0
  return {
    message: placed && !unplaced ? 'Fix the highlighted fields and try again.' : GENERIC_INVALID,
    fields,
    retryable: false,
  }
}
