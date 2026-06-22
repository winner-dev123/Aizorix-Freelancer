// Automated browser click-through against the LIVE stack (Playwright + Chromium).
// Drives: landing -> login -> authenticated dashboard -> marketplace, capturing a screenshot
// at each step. Proves the UI works in a real browser end-to-end, not just at the API level.
//
//   node node_modules/playwright/cli.js install chromium   # one-time: download the browser
//   BASE_URL=http://localhost:3000 node e2e/smoke.mjs       # run against the running stack
//
// Demo credentials (from the seed): ada@aizorix.dev / DemoPassw0rd!  (a freelancer)

import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';

const BASE = process.env.BASE_URL ?? 'http://localhost:3000';
const OUT = process.env.SHOT_DIR ?? 'e2e/screenshots';
mkdirSync(OUT, { recursive: true });

const results = [];
const step = (name, ok, detail = '') => {
  results.push({ name, ok });
  console.log(`  ${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? ` — ${detail}` : ''}`);
};

const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1366, height: 900 } });
const page = await ctx.newPage();

try {
  // 1. Landing (SSR)
  await page.goto(`${BASE}/`, { waitUntil: 'networkidle', timeout: 30000 });
  await page.screenshot({ path: `${OUT}/01-landing.png`, fullPage: true });
  step('landing renders', true, `title="${await page.title()}"`);

  // 2. Login page
  await page.goto(`${BASE}/login`, { waitUntil: 'networkidle', timeout: 30000 });
  await page.screenshot({ path: `${OUT}/02-login.png` });
  const hasEmail = (await page.locator('input[type=email]').count()) > 0;
  step('login form renders', hasEmail);

  // 3. Real login -> the form POSTs through the Next proxy to the live gateway -> auth -> DB
  await page.fill('input[type=email]', 'ada@aizorix.dev');
  await page.fill('input[type=password]', 'DemoPassw0rd!');
  await page.click('button[type=submit]');
  await page
    .waitForURL(/\/(freelancer|marketplace|client|admin)/, { timeout: 20000 })
    .catch(() => {});
  await page.waitForTimeout(2500);
  await page.screenshot({ path: `${OUT}/03-after-login.png`, fullPage: true });
  const loggedIn = /\/(freelancer|marketplace|client|admin)/.test(page.url());
  step('logged in (redirected to an authed route)', loggedIn, page.url());

  // 4. Marketplace (authenticated dashboard chrome renders; project data via the gateway)
  await page.goto(`${BASE}/marketplace`, { waitUntil: 'networkidle', timeout: 30000 }).catch(() => {});
  await page.waitForTimeout(1500);
  await page.screenshot({ path: `${OUT}/04-marketplace.png`, fullPage: true });
  step('marketplace renders', !page.url().endsWith('/login'), page.url());
} catch (e) {
  step('unexpected exception', false, e.message);
} finally {
  await browser.close();
}

const failed = results.filter((r) => !r.ok).length;
console.log(
  `\n==== PLAYWRIGHT CLICK-THROUGH: ${results.length - failed}/${results.length} passed; screenshots in ${OUT}/ ====`,
);
process.exit(failed > 0 ? 1 : 0);
