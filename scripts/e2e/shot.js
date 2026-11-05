// Screenshot one element of a portal page as a logged-in operator.
// Usage: node shot.js <base> <cookies-json> <path> <selector> <out.png>
// Used by the e2e scripts when E2E_SHOTS is set; needs Playwright.
const { chromium } = require('playwright');

(async () => {
  const [base, cookiesJSON, path, selector, out] = process.argv.slice(2);
  const browser = await chromium.launch();
  try {
    const ctx = await browser.newContext({
      viewport: { width: 1280, height: 900 }, deviceScaleFactor: 2, colorScheme: 'light',
    });
    const url = new URL(base);
    await ctx.addCookies(JSON.parse(cookiesJSON).map(c => ({
      name: c.name, value: c.value, domain: url.hostname, path: '/',
    })));
    const page = await ctx.newPage();
    await page.goto(base + path, { waitUntil: 'networkidle' });
    const el = page.locator(selector).first();
    await el.scrollIntoViewIfNeeded();
    await el.screenshot({ path: out, animations: 'disabled' });
  } finally {
    await browser.close();
  }
})().catch(e => { console.error(e.message); process.exit(1); });
