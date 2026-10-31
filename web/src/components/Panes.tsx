import { Panel, PanelGroup, PanelResizeHandle } from 'react-resizable-panels'
import clsx from 'clsx'
import { useWideLayout } from '../hooks/useWideLayout'
import { useI18n } from '../i18n'

/** Desktop: two resizable panes (size remembered). Narrow: tabs, so nothing is lost. */
export function Panes({
  left,
  right,
  mobileTab,
}: {
  left: React.ReactNode
  right: React.ReactNode
  mobileTab?: { value: 'original' | 'markdown'; onChange: (v: 'original' | 'markdown') => void }
}) {
  const wide = useWideLayout()
  const { t } = useI18n()
  if (wide) {
    return (
      <PanelGroup direction="horizontal" autoSaveId="parsedock-playground-panes" className="h-full">
        <Panel defaultSize={50} minSize={25} className="min-h-0">
          {left}
        </Panel>
        <PanelResizeHandle
          className={clsx(
            'group w-1.5 bg-ink-200 transition-colors hover:bg-accent-500/70 data-[resize-handle-state=drag]:bg-accent-500 dark:bg-ink-800',
          )}
          aria-label={t('panes.resize')}
        />
        <Panel defaultSize={50} minSize={25} className="min-h-0">
          {right}
        </Panel>
      </PanelGroup>
    )
  }
  const tab = mobileTab?.value ?? 'original'
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div
        role="tablist"
        aria-label={t('panes.workspace')}
        className="flex border-b border-ink-200 dark:border-ink-800"
      >
        {(['original', 'markdown'] as const).map((tabId) => (
          <button
            key={tabId}
            role="tab"
            aria-selected={tab === tabId}
            type="button"
            onClick={() => mobileTab?.onChange(tabId)}
            className={clsx(
              'flex-1 px-3 py-2 text-xs font-medium',
              tab === tabId
                ? 'border-b-2 border-accent-600 text-ink-900 dark:text-white'
                : 'text-ink-500',
            )}
          >
            {tabId === 'original' ? t('panes.original') : t('panes.markdown')}
          </button>
        ))}
      </div>
      <div className="min-h-0 flex-1">{tab === 'original' ? left : right}</div>
    </div>
  )
}
