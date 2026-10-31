import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { MarkdownPane } from '../components/MarkdownPane'

const HOSTILE = `
# Report

<script>window.__pwned = true</script>

<img src="https://tracker.example/pixel.png" onerror="window.__pwned = true">

<iframe src="https://evil.example"></iframe>

[click me](javascript:window.__pwned=true)

![remote pixel](https://tracker.example/2.png)

A real paragraph with **bold** and a [safe link](https://example.com/docs).

| a | b |
| --- | --- |
| 1 | 2 |
`

function renderPane(markdown: string) {
  return render(
    <MarkdownPane
      markdown={markdown}
      fileName="report.md"
      phase="completed"
      onRelease={() => {}}
      canRelease={false}
    />,
  )
}

describe('Markdown rendering safety', () => {
  it('renders GFM tables and text', () => {
    renderPane(HOSTILE)
    expect(screen.getByRole('table')).toBeInTheDocument()
    expect(screen.getByText(/A real paragraph with/)).toBeInTheDocument()
    expect(screen.getByText('bold')).toBeInTheDocument()
  })

  it('never executes document HTML or scripts', () => {
    const { container } = renderPane(HOSTILE)
    expect(container.querySelector('script')).toBeNull()
    expect(container.querySelector('iframe')).toBeNull()
    expect((window as unknown as { __pwned?: boolean }).__pwned).toBeUndefined()
    // The script/iframe source is shown as text, not executed.
    expect(container.textContent).not.toContain('window.__pwned = true')
  })

  it('never loads remote images', () => {
    const { container } = renderPane(HOSTILE)
    expect(container.querySelectorAll('img')).toHaveLength(0)
    // A placeholder carries the alt text instead, without a network request.
    expect(container.textContent).toContain('remote pixel')
  })

  it('strips javascript: links but keeps safe ones', () => {
    const { container } = renderPane(HOSTILE)
    const hrefs = Array.from(container.querySelectorAll('a')).map((a) => a.getAttribute('href') ?? '')
    expect(hrefs.some((h) => h.toLowerCase().startsWith('javascript:'))).toBe(false)
    const safe = container.querySelector('a[href="https://example.com/docs"]')
    expect(safe).not.toBeNull()
    expect(safe).toHaveAttribute('rel', expect.stringContaining('noopener'))
    expect(safe).toHaveAttribute('target', '_blank')
  })

  it('switches to the byte-exact source view', async () => {
    renderPane(HOSTILE)
    await userEvent.click(screen.getByRole('tab', { name: 'Markdown source' }))
    const pre = document.querySelector('pre')
    expect(pre?.textContent).toBe(HOSTILE)
  })
})
