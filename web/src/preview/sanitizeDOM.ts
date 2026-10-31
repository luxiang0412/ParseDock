const FORBIDDEN = new Set(['SCRIPT', 'IFRAME', 'OBJECT', 'EMBED', 'LINK', 'META', 'BASE', 'FORM'])

/**
 * Defence in depth for rendered Office previews: docx-preview builds live DOM from a
 * user-supplied package, so before it reaches the page we drop active content and inline
 * handlers. Inline `style` is kept (it is what makes the preview look right) but URLs are
 * restricted to non-scripting schemes.
 */
export function stripActiveContent(root: ParentNode): void {
  root.querySelectorAll('*').forEach((el) => {
    if (FORBIDDEN.has(el.tagName)) {
      el.remove()
      return
    }
    for (const attr of Array.from(el.attributes)) {
      const name = attr.name.toLowerCase()
      if (name.startsWith('on')) {
        el.removeAttribute(attr.name)
        continue
      }
      if (name === 'href' || name === 'src' || name === 'xlink:href' || name === 'action') {
        const value = attr.value.trim().toLowerCase()
        if (value.startsWith('javascript:') || value.startsWith('vbscript:') || value.startsWith('data:text/html')) {
          el.removeAttribute(attr.name)
        }
      }
      if (name === 'srcset') el.removeAttribute(attr.name)
    }
  })
}
