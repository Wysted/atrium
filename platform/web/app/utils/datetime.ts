/** Date helpers for grants. The API speaks RFC 3339; the UI shows the user's locale and offset. */

const formatter = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })

/** Locale-formatted date and time of an RFC 3339 string; the raw value when it does not parse. */
export function formatTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : formatter.format(date)
}

/** True when `value` (RFC 3339) is at or before `now`. */
export function isPast(value: string, now: number = Date.now()): boolean {
  const time = new Date(value).getTime()
  return !Number.isNaN(time) && time <= now
}

const LOCAL_INPUT = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/

function pad(n: number): string {
  return String(n).padStart(2, '0')
}

export type LocalInputResult =
  | { ok: true, value: string }
  | { ok: false, reason: 'invalid' | 'nonexistent' }

/**
 * Converts the value of an `<input type="datetime-local">` (wall-clock time of the user, no zone) to RFC 3339 with
 * the user's offset at that instant (`2026-10-20T14:30:00-03:00`). `invalid` when the value does not parse or is an
 * impossible date; `nonexistent` when the wall-clock time is skipped by a DST spring-forward in the user's zone.
 * An ambiguous time (DST fall-back, it happens twice) resolves to its first occurrence, the one `new Date` picks.
 */
export function localInputToRfc3339(value: string): LocalInputResult {
  const match = LOCAL_INPUT.exec(value)
  if (!match) return { ok: false, reason: 'invalid' }
  const [year, month, day, hour, minute] = match.slice(1, 6).map(Number) as [number, number, number, number, number]
  const second = Number(match[6] ?? 0)
  const date = new Date(year, month - 1, day, hour, minute, second)
  // `new Date` rolls impossible dates over (31 February): reject them.
  if (date.getFullYear() !== year || date.getMonth() !== month - 1 || date.getDate() !== day) {
    return { ok: false, reason: 'invalid' }
  }
  // A skipped DST wall-clock time is shifted forward: the hour or minute no longer match.
  if (date.getHours() !== hour || date.getMinutes() !== minute) return { ok: false, reason: 'nonexistent' }
  const offset = -date.getTimezoneOffset()
  const sign = offset >= 0 ? '+' : '-'
  const abs = Math.abs(offset)
  return {
    ok: true,
    value: `${match[1]}-${match[2]}-${match[3]}T${match[4]}:${match[5]}:${pad(second)}${sign}${pad(Math.floor(abs / 60))}:${pad(abs % 60)}`,
  }
}
