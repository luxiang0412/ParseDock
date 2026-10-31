/**
 * ParseDock's Markdown view writes two pieces of HTML on purpose: `<br>` for a line break
 * inside a pipe-table cell, and an HTML `<table>` (rowspan/colspan, `<th>` header rows)
 * for a table with merged cells, which GFM pipe tables cannot express. rehype-sanitize
 * drops every raw HTML node, so without this step both disappeared from the rendered view
 * (the source view always shows the bytes).
 *
 * This runs *before* rehype-sanitize and converts only those two shapes into ordinary hast
 * elements, rebuilding them from a fixed whitelist: table/thead/tbody/tfoot/tr/th/td/br,
 * integer rowspan/colspan, and text. Every other tag inside a table is unwrapped to its
 * text; every attribute other than the spans is dropped; any other raw HTML is left for
 * the sanitizer to remove. The HTML is parsed with DOMParser, which builds an inert
 * document (no scripts run, nothing is fetched), and no parsed DOM node is ever inserted
 * into the page. rehype-sanitize still runs on the result.
 */

interface HastText {
  type: 'text'
  value: string
}

interface HastElement {
  type: 'element'
  tagName: string
  properties: Record<string, number>
  children: HastChild[]
}

type HastChild = HastText | HastElement

interface HastNode {
  type: string
  value?: string
  children?: HastNode[]
}

const TABLE_TAGS = new Set(['table', 'thead', 'tbody', 'tfoot', 'tr', 'th', 'td'])
// Elements whose content is code or embedded documents, never cell text.
const OPAQUE_TAGS = new Set([
  'script',
  'style',
  'template',
  'noscript',
  'iframe',
  'object',
  'svg',
  'math',
])
const BR = /^<br\s*\/?>$/i
const MAX_SPAN = 1000

export function rehypeHtmlTables() {
  return (tree: HastNode) => {
    visit(tree)
  }
}

function visit(node: HastNode): void {
  const kids = node.children
  if (!kids) return
  for (let i = 0; i < kids.length; i++) {
    const child = kids[i]
    if (!child) continue
    if (child.type === 'raw' && typeof child.value === 'string') {
      const replacement = convert(child.value.trim())
      if (replacement) kids[i] = replacement
      continue
    }
    visit(child)
  }
}

function convert(html: string): HastChild | null {
  if (BR.test(html)) return element('br', {}, [])
  const lower = html.toLowerCase()
  if (!lower.startsWith('<table') || !lower.endsWith('</table>')) return null
  if (typeof DOMParser === 'undefined') return null
  const doc = new DOMParser().parseFromString(html, 'text/html')
  const table = doc.body.querySelector('table')
  if (!table || doc.body.children.length !== 1) return null
  return fromDOM(table)
}

function fromDOM(el: Element): HastElement {
  const tag = el.tagName.toLowerCase()
  const props: Record<string, number> = {}
  if (tag === 'td' || tag === 'th') {
    const rs = span(el.getAttribute('rowspan'))
    const cs = span(el.getAttribute('colspan'))
    if (rs > 1) props.rowSpan = rs
    if (cs > 1) props.colSpan = cs
  }
  return element(tag, props, childrenOf(el, tag))
}

function childrenOf(el: Element, tag: string): HastChild[] {
  const out: HastChild[] = []
  const inCell = tag === 'td' || tag === 'th'
  el.childNodes.forEach((n) => {
    if (n.nodeType === Node.TEXT_NODE) {
      // Whitespace between rows is formatting; text belongs to cells only.
      if (inCell && n.textContent) out.push({ type: 'text', value: n.textContent })
      return
    }
    if (n.nodeType !== Node.ELEMENT_NODE) return
    const child = n as Element
    const childTag = child.tagName.toLowerCase()
    if (OPAQUE_TAGS.has(childTag)) return
    if (inCell) {
      if (childTag === 'br') out.push(element('br', {}, []))
      else if (child.textContent) out.push({ type: 'text', value: child.textContent })
      return
    }
    if (TABLE_TAGS.has(childTag)) out.push(fromDOM(child))
  })
  return out
}

function span(v: string | null): number {
  const n = Number.parseInt(v ?? '', 10)
  if (!Number.isFinite(n) || n < 1) return 1
  return Math.min(n, MAX_SPAN)
}

function element(
  tagName: string,
  properties: Record<string, number>,
  children: HastChild[],
): HastElement {
  return { type: 'element', tagName, properties, children }
}
