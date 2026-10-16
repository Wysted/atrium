/** Shared prop types for base components. Values map to token-based Tailwind classes. */

/** Steps of the spacing scale (multiples of --spacing). */
export type Space = 0 | 1 | 2 | 3 | 4 | 6 | 8 | 12

export type TextSize = 'xs' | 'sm' | 'base' | 'lg' | 'xl' | '2xl' | '3xl' | '4xl'
export type TextTone = 'default' | 'muted' | 'primary' | 'danger' | 'warning' | 'success'
export type TextWeight = 'normal' | 'medium' | 'semibold' | 'bold'

export type Align = 'start' | 'center' | 'end' | 'stretch' | 'baseline'
export type Justify = 'start' | 'center' | 'end' | 'between'

export type HeadingLevel = 1 | 2 | 3 | 4 | 5 | 6

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger'
export type ButtonSize = 'sm' | 'md' | 'lg'

export type IconName =
  | 'check'
  | 'loader'
  | 'x'
  | 'plus'
  | 'minus'
  | 'chevron-down'
  | 'chevron-right'
  | 'external-link'
  | 'alert-triangle'
  | 'info'
  | 'search'
