import type { Mode } from '../../api/client'
import type { MessageKey } from '../../i18n/messages'

export const MODES: Mode[] = ['auto', 'fast', 'accurate']

export const MODE_LABEL: Record<Mode, MessageKey> = {
  auto: 'mode.auto',
  fast: 'mode.fast',
  accurate: 'mode.accurate',
}

export const MODE_HINT: Record<Mode, MessageKey> = {
  auto: 'mode.auto.hint',
  fast: 'mode.fast.hint',
  accurate: 'mode.accurate.hint',
}

export const MODE_NOTE: Record<Mode, MessageKey> = {
  auto: 'mode.auto.note',
  fast: 'mode.fast.note',
  accurate: 'mode.accurate.note',
}
