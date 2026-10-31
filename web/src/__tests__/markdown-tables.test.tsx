import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { MarkdownPane } from '../components/MarkdownPane'
import { I18nProvider } from '../i18n'

// What the server's Markdown view emits for a table with merged cells, and for a line
// break inside a pipe-table cell (internal/documentir/export.go).
const SPANS = `# Merged

<table>
<thead>
<tr><th colspan="2">Merged &lt;head&gt;</th><th>C</th></tr>
</thead>
<tbody>
<tr><td rowspan="2">tall</td><td>b1<br>b2</td><td>c1</td></tr>
<tr><td>b3</td><td></td></tr>
</tbody>
</table>

| Region | Note |
| --- | --- |
| North | line one<br>line two |
`

const HOSTILE_TABLE = `<table>
<tr><td onclick="window.__pwned=true" rowspan="99999" style="color:red"><img src="https://tracker.example/p.png" onerror="window.__pwned=true">hi<script>window.__pwned=true</script></td></tr>
</table>
`

function renderPane(markdown: string) {
  return render(
    <I18nProvider initialLocale="en">
      <MarkdownPane
        markdown={markdown}
        fileName="r.md"
        phase="completed"
        onRelease={() => {}}
        canRelease={false}
      />
    </I18nProvider>,
  )
}

describe('Markdown tables from the IR renderer', () => {
  it('renders merged cells and header rows from the HTML table', () => {
    const { container } = renderPane(SPANS)
    const tables = container.querySelectorAll('table')
    expect(tables).toHaveLength(2)
    const first = tables[0]
    if (!first) throw new Error('no table')
    const head = first.querySelector('th')
    expect(head).toHaveAttribute('colspan', '2')
    expect(head?.textContent).toBe('Merged <head>')
    const tall = Array.from(first.querySelectorAll('td')).find((td) => td.textContent === 'tall')
    expect(tall).toHaveAttribute('rowspan', '2')
    expect(first.querySelectorAll('br')).toHaveLength(1)
  })

  it('renders <br> inside pipe-table cells as a line break', () => {
    const { container } = renderPane(SPANS)
    const cell = Array.from(container.querySelectorAll('td')).find(
      (td) => td.textContent === 'line oneline two',
    )
    expect(cell).toBeDefined()
    expect(cell?.querySelector('br')).not.toBeNull()
  })

  it('rebuilds a table from a whitelist and drops everything else', () => {
    const { container } = renderPane(HOSTILE_TABLE)
    const td = container.querySelector('td')
    expect(td?.textContent).toBe('hi')
    expect(td).not.toHaveAttribute('onclick')
    expect(td).not.toHaveAttribute('style')
    expect(td).toHaveAttribute('rowspan', '1000')
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('script')).toBeNull()
    expect((window as unknown as { __pwned?: boolean }).__pwned).toBeUndefined()
  })

  it('still drops any other raw HTML', () => {
    const { container } = renderPane('<div onclick="x">block</div>\n\ntext <span>inline</span>\n')
    const article = container.querySelector('article')
    expect(article?.querySelector('div')).toBeNull()
    expect(article?.querySelector('span')).toBeNull()
    expect(article?.textContent).toContain('text')
  })
})
