import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { Dropzone } from '../components/Dropzone'
import { I18nProvider, interpolate, MESSAGES, translate } from '../i18n'
import { en } from '../i18n/messages'

describe('dictionaries', () => {
  it('have exactly the same keys', () => {
    const enKeys = Object.keys(en).sort()
    const zhKeys = Object.keys(MESSAGES['zh-CN']).sort()
    expect(zhKeys).toEqual(enKeys)
  })

  it('leave no untranslated English placeholder in zh-CN', () => {
    // A handful of strings are intentionally identical (product name, language names).
    const allowed = new Set([
      'app.name',
      'lang.en',
      'lang.zh',
      'source.kind.pdf',
      'source.kind.markdown',
      'source.kind.html',
      'panes.markdown',
    ])
    const same = Object.keys(en).filter(
      (k) => !allowed.has(k) && (en as Record<string, string>)[k] === MESSAGES['zh-CN'][k as keyof typeof en],
    )
    expect(same).toEqual([])
  })

  it('interpolates placeholders and leaves unknown ones alone', () => {
    expect(interpolate('in {value}', { value: '42 min' })).toBe('in 42 min')
    expect(interpolate('{missing}', {})).toBe('{missing}')
    expect(translate('zh-CN', 'status.expiresIn', { when: '42 分钟后' })).toBe(
      '服务端副本将于42 分钟后过期',
    )
  })
})

describe('<I18nProvider>', () => {
  it('renders English by default for an en locale', () => {
    render(
      <I18nProvider initialLocale="en">
        <Dropzone onSelect={() => {}} />
      </I18nProvider>,
    )
    expect(screen.getByText(/Drop a document here/)).toBeInTheDocument()
  })

  it('renders Chinese when asked', () => {
    render(
      <I18nProvider initialLocale="zh-CN">
        <Dropzone onSelect={() => {}} />
      </I18nProvider>,
    )
    expect(screen.getByText(/把文档拖到这里/)).toBeInTheDocument()
    expect(screen.getByText(/选择文件/)).toBeInTheDocument()
  })

  it('props a Chinese rejection message through the translator', async () => {
    render(
      <I18nProvider initialLocale="zh-CN">
        <Dropzone onSelect={() => {}} />
      </I18nProvider>,
    )
    const input = document.querySelector('input[type=file]') as HTMLInputElement
    await userEvent.upload(input, new File(['x'], 'a.zip', { type: 'application/zip' }), {
      applyAccept: false,
    })
    expect(await screen.findByRole('alert')).toHaveTextContent('a.zip 不是支持的类型')
  })
})
