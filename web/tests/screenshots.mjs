// Screenshot verification: logs into a running qbit-history (default
// http://127.0.0.1:28637) and captures every view at 390 / 768 / 1440 px in
// light and dark themes. Requires: `npx playwright install chromium`.
//
//   HISTORY_URL=http://127.0.0.1:28637 HISTORY_PASSWORD=... node tests/screenshots.mjs
import { chromium } from '@playwright/test'
import { mkdirSync } from 'node:fs'

const base = process.env.HISTORY_URL || 'http://127.0.0.1:28637'
const password = process.env.HISTORY_PASSWORD || 'local-demo-password'
const out = process.env.SCREENSHOT_DIR || '../docs/screenshots'
mkdirSync(out, { recursive: true })

const widths = [390, 768, 1440]
const themes = ['light', 'dark']
const views = [
  { name: 'overview', path: '/' },
  { name: 'torrents', path: '/torrents' },
  { name: 'connections', path: '/connections' },
  { name: 'settings', path: '/settings' },
  { name: 'status', path: '/status' }
]
const browser = await chromium.launch()
const failures = []
for (const theme of themes) {
  for (const width of widths) {
    const context = await browser.newContext({ viewport: { width, height: width < 600 ? 844 : 900 }, deviceScaleFactor: 1, hasTouch: width < 600, colorScheme: theme })
    const page = await context.newPage()
    page.on('pageerror', (e) => failures.push(`${theme}/${width}: page error ${e.message}`))
    page.on('console', (m) => {
      // The pre-login /auth/session probe answers 401 by design; everything else is a real error.
      if (m.type() === 'error' && !m.text().includes('401')) failures.push(`${theme}/${width}: console ${m.text()}`)
    })
    await page.addInitScript((t) => localStorage.setItem('qh.theme', t), theme)
    await page.goto(base + '/')
    await page.locator('input[autocomplete="current-password"]').fill(password)
    await page.locator('form button[type="submit"]').click()
    await page.waitForSelector('.v-navigation-drawer', { timeout: 15000 })
    for (const v of views) {
      await page.goto(base + v.path)
      await page.waitForTimeout(1800)
      await page.screenshot({ path: `${out}/${v.name}-${theme}-${width}.png`, fullPage: true })
    }
    // Detail view: first torrent from the list.
    await page.goto(base + '/torrents')
    await page.waitForTimeout(1500)
    const row = page.locator('.torrent-name').first()
    if (await row.count()) {
      await row.click()
      await page.waitForTimeout(2500)
      await page.screenshot({ path: `${out}/torrent-detail-${theme}-${width}.png`, fullPage: true })
      // Zoom interaction: switch to cumulative and to the 15m preset.
      await page.getByRole('button', { name: '累计量' }).click().catch(() => {})
      await page.waitForTimeout(1200)
      await page.screenshot({ path: `${out}/torrent-detail-cumulative-${theme}-${width}.png`, fullPage: false })
    } else {
      failures.push(`${theme}/${width}: no torrent rows to open`)
    }
    await context.close()
  }
}
await browser.close()
if (failures.length) {
  console.error('Issues:\n' + failures.join('\n'))
  process.exit(1)
}
console.log(`screenshots written to ${out}`)
