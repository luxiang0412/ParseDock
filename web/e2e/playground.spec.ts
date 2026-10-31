import { expect, test, type Page } from '@playwright/test'
import { docx, longPdf, pdf, txt, xlsx, zipFixture } from './fixtures'

const hasXberg = (process.env.E2E_ENGINES ?? '').includes('xberg')
const SHOTS = '../docs/screenshots'

async function upload(page: Page, filePath: string | { name: string; mimeType: string; buffer: Buffer }, name?: string) {
  const input = page.locator('input[type=file]')
  if (typeof filePath === 'string') {
    await input.setInputFiles(filePath)
  } else {
    await input.setInputFiles({ name: filePath.name, mimeType: filePath.mimeType, buffer: filePath.buffer })
  }
  if (name) await expect(page.getByText(name, { exact: false }).first()).toBeVisible()
}

/** On a narrow screen the workspace is tabbed; bring the original pane into view. */
async function showOriginal(page: Page) {
  const tab = page.getByRole('tab', { name: 'Original' })
  if (await tab.count()) await tab.click()
}

async function showMarkdown(page: Page) {
  const tab = page.getByRole('tab', { name: 'Markdown', exact: true })
  if (await tab.count()) await tab.click()
}

async function parse(page: Page) {
  await page.getByRole('button', { name: 'Parse', exact: true }).click()
}

test.describe('Playground', () => {
  test('idle state is a pristine workbench', async ({ page }, info) => {
    await page.goto('/')
    await expect(page.getByRole('heading', { name: /ParseDock Playground/i })).toBeVisible()
    await expect(page.getByText(/Drop a document here/)).toBeVisible()
    await expect(page.getByRole('radiogroup', { name: 'Parse mode' })).toBeVisible()
    if (info.project.name === 'desktop') {
      await page.screenshot({ path: `${SHOTS}/playground-idle.png`, fullPage: false })
    }
  })

  test('text file: parse, compare, copy, download and release', async ({ page }, info) => {
    await page.goto('/')
    await upload(page, txt, 'txt_plain.txt')
    // The original is previewed locally before any request is made.
    await expect(page.locator('section[aria-label="Original document"]')).toBeVisible()
    await parse(page)

    await expect(page.getByTestId('job-status')).toContainText(/Parsing|Queued|Completed/)
    await expect(page.getByRole('tab', { name: 'Preview' })).toBeVisible({ timeout: 60_000 })
    await expect(page.getByRole('tab', { name: 'Markdown source' })).toBeEnabled()

    // The markdown pane shows what the API returned, not a mock.
    await page.getByRole('tab', { name: 'Markdown source' }).click()
    const source = await page.locator('pre').first().innerText()
    expect(source.length).toBeGreaterThan(10)

    await page.getByRole('button', { name: 'Copy', exact: true }).click()
    await expect(page.getByRole('button', { name: 'Copied', exact: true })).toBeVisible()

    const download = page.waitForEvent('download')
    await page.getByRole('button', { name: 'Download .md', exact: true }).click()
    expect((await download).suggestedFilename()).toBe('txt_plain.md')

    if (info.project.name === 'desktop') {
      await page.getByRole('tab', { name: 'Preview' }).click()
      await page.screenshot({ path: `${SHOTS}/playground-result-text.png` })
    }

    // Explicit release: 202 then the tombstone becomes visible.
    await page.getByTestId('job-status').getByRole('button', { name: 'Release server copy' }).click()
    await expect(page.getByTestId('job-status')).toContainText(/released|deletion in progress/i, { timeout: 45_000 })
    await expect(page.getByRole('tab', { name: 'Preview' })).toBeVisible()
  })

  test('rejected type shows an actionable error and no job starts', async ({ page }) => {
    await page.goto('/')
    await upload(page, { name: 'archive.zip', mimeType: 'application/zip', buffer: zipFixture() })
    // The drop zone rejects it client-side before any upload.
    await expect(page.getByRole('alert')).toContainText(/not an accepted type/i)
    await expect(page.getByRole('button', { name: 'Parse', exact: true })).toHaveCount(0)
  })

  test('switching files while a result is held asks first', async ({ page }) => {
    await page.goto('/')
    await upload(page, txt, 'txt_plain.txt')
    await parse(page)
    await expect(page.getByTestId('job-status')).toContainText('Completed', { timeout: 60_000 })
    await upload(page, txt)
    await expect(page.getByRole('dialog')).toContainText('Replace the current document?')
    await page.getByRole('button', { name: 'Replace' }).click()
    await expect(page.getByRole('dialog')).toHaveCount(0)
  })

  test('mobile layout keeps both panes reachable', async ({ page }, info) => {
    test.skip(info.project.name !== 'mobile', 'mobile-only layout check')
    await page.goto('/')
    await upload(page, txt, 'txt_plain.txt')
    await parse(page)
    await expect(page.getByRole('tab', { name: 'Markdown', exact: true })).toBeVisible({ timeout: 60_000 })
    await expect(page.getByTestId('job-status')).toContainText('Completed', { timeout: 60_000 })
    // The result tab is selected automatically on a narrow screen.
    await expect(page.getByRole('tab', { name: 'Preview' })).toBeVisible()
    await expect(page.locator('article.md')).toContainText(/.+/)
    await page.screenshot({ path: `${SHOTS}/playground-mobile.png`, fullPage: false })
  })

  test('the proxy does not expose internal endpoints', async ({ request }) => {
    const res = await request.get('/internal/metrics')
    expect(res.status()).toBe(404)
    expect(await res.text()).not.toContain('parsedock_')
  })

  test('real PDF: preview, long markdown and a stable screenshot', async ({ page }, info) => {
    test.skip(!hasXberg, 'needs the xberg engine (E2E_ENGINES=native,xberg)')
    await page.goto('/')
    await upload(page, pdf, 'pdf_text_en_3p.pdf')
    await parse(page)
    await expect(page.getByTestId('job-status')).toContainText('Completed', { timeout: 120_000 })
    // Left: pdf.js renders the document to canvases (works in headless browsers too).
    await showOriginal(page)
    await expect(page.locator('section[aria-label="Original document"] canvas').first()).toBeVisible({ timeout: 30_000 })
    await expect(page.locator('section[aria-label="Original document"]')).toContainText('pages')
    await showMarkdown(page)
    const body = page.locator('article.md')
    await expect(body).toContainText(/Quarterly Logistics Review/i)
    await expect(body.getByRole('table')).toHaveCount(1)
    if (info.project.name === 'desktop') {
      await page.screenshot({ path: `${SHOTS}/playground-result-pdf.png` })
    }
  })

  test('long markdown scrolls without breaking the layout', async ({ page }, info) => {
    test.skip(!hasXberg, 'needs the xberg engine')
    await page.goto('/')
    await upload(page, longPdf, 'pdf_text_en_30p.pdf')
    await parse(page)
    await expect(page.getByTestId('job-status')).toContainText('Completed', { timeout: 180_000 })
    const pane = page.locator('article.md').first()
    await expect(pane).toBeVisible()
    await expect(pane.locator('p').nth(2)).toContainText(/./)
    if (info.project.name === 'desktop') {
      await page.screenshot({ path: `${SHOTS}/playground-long-markdown.png` })
    }
  })

  test('office previews are best-effort and never execute content', async ({ page }, info) => {
    test.skip(!hasXberg, 'needs an engine that parses Office')
    await page.goto('/')
    await upload(page, docx, 'docx_report.docx')
    await parse(page)
    await expect(page.getByTestId('job-status')).toContainText('Completed', { timeout: 120_000 })
    await showOriginal(page)
    await expect(page.locator('.docx-wrap')).toBeVisible({ timeout: 30_000 })
    if (info.project.name === 'desktop') {
      await page.screenshot({ path: `${SHOTS}/playground-docx.png` })
    }

    await page.goto('/')
    await upload(page, xlsx, 'xlsx_workbook.xlsx')
    await parse(page)
    await expect(page.getByTestId('job-status')).toContainText('Completed', { timeout: 120_000 })
    // Sheet tabs come from the workbook itself, and the first sheet renders as a grid.
    await showOriginal(page)
    await expect(page.getByRole('button', { name: 'Summary' })).toBeVisible({ timeout: 30_000 })
    await expect(page.getByRole('button', { name: 'Detail' })).toBeVisible()
    await expect(page.locator('section[aria-label="Original document"] table').first()).toBeVisible()
  })
})
