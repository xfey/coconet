import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, writeFile, mkdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const { chromium } = await import(pathToFileURL(resolve(process.env.COCONET_PLAYWRIGHT_MODULE)).href);
const temporary = await mkdtemp(join(tmpdir(), 'coconet-login-ripple-'));
const fixturePath = join(temporary, 'fixture.json');
const helper = spawn('go', ['test', './apps/server', '-run', '^TestUnifiedLoginBrowserHarness$', '-count=1', '-timeout=4m'], {
  env:{ ...process.env, COCONET_ACCOUNT_BROWSER_FIXTURE:fixturePath }, stdio:['ignore', 'pipe', 'pipe'],
});
let output = '', browser;
helper.stdout.on('data', chunk => { output += chunk; }); helper.stderr.on('data', chunk => { output += chunk; });
const done = new Promise(resolve => helper.on('exit', resolve));
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
const screenshotDirectory = process.env.COCONET_RIPPLE_SCREENSHOTS;
async function screenshot(page, name) {
  if (!screenshotDirectory) return;
  await mkdir(screenshotDirectory, { recursive:true });
  await page.screenshot({ path:join(screenshotDirectory, name + '.png') });
}
async function expectStopped(page) {
  await delay(80);
  const frames = await page.evaluate(() => window.rippleFrames);
  await delay(180);
  assert.equal(await page.evaluate(() => window.rippleFrames), frames);
}
async function expectRunning(page) {
  const frames = await page.evaluate(() => window.rippleFrames);
  await page.waitForFunction(previous => window.rippleFrames > previous + 1, frames);
}
try {
  let fixture;
  for (let attempt = 0; attempt < 400; attempt++) {
    if (helper.exitCode !== null) throw new Error(output);
    try { fixture = JSON.parse(await readFile(fixturePath, 'utf8')); break; } catch { await delay(100); }
  }
  assert.ok(fixture, 'production handler fixture did not start');
  browser = await chromium.launch({ headless:true, executablePath:process.env.COCONET_BROWSER_EXECUTABLE });
  const context = await browser.newContext({ ignoreHTTPSErrors:true, locale:'en-US', deviceScaleFactor:2, viewport:{ width:1440, height:960 } });
  await context.addInitScript(() => {
    window.rippleFrames = 0; window.rippleCSP = [];
    const clear = CanvasRenderingContext2D.prototype.clearRect;
    CanvasRenderingContext2D.prototype.clearRect = function (...args) {
      if (this.canvas.id === 'login-ripple') window.rippleFrames++;
      return clear.apply(this, args);
    };
    document.addEventListener('securitypolicyviolation', event => window.rippleCSP.push(event.violatedDirective));
  });
  const page = await context.newPage(), errors = [];
  context.on('page', child => child.on('pageerror', error => errors.push(error.message)));
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(fixture.url);
  await page.locator('.login-visual[data-ripple-ready]').waitFor();
  await expectRunning(page);
  assert.equal(await page.locator('#motion-toggle, .preview-controls').count(), 0);
  assert.equal(await page.locator('#login-ripple').getAttribute('tabindex'), null);

  for (const width of [2560, 1440, 1024, 768, 390, 320]) {
    await page.setViewportSize({ width, height:width > 760 ? 960 : 844 });
    for (const language of ['zh', 'en']) {
      await page.locator('#login [data-language]').selectOption(language);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      assert.equal(await page.locator('.login-visual').isVisible(), width > 760);
      if (width > 760) await expectRunning(page); else await expectStopped(page);
      await screenshot(page, `login-${width}-${language}`);
    }
  }
  await page.setViewportSize({ width:1440, height:960 });
  await expectRunning(page);
  await page.emulateMedia({ reducedMotion:'reduce' });
  await expectStopped(page);
  await screenshot(page, 'reduced-motion');
  await page.emulateMedia({ reducedMotion:'no-preference' });
  await expectRunning(page);

  await page.evaluate(() => {
    Object.defineProperty(document, 'hidden', { configurable:true, get:() => true });
    document.dispatchEvent(new Event('visibilitychange'));
  });
  await expectStopped(page);
  await page.evaluate(() => { delete document.hidden; document.dispatchEvent(new Event('visibilitychange')); });
  await expectRunning(page);
  await page.evaluate(() => window.dispatchEvent(new Event('pagehide')));
  await expectStopped(page);
  await page.evaluate(() => window.dispatchEvent(new Event('pageshow')));
  await expectRunning(page);

  // A real fixture OAuth round trip hides the login and must stop rendering.
  await page.locator('#provider-login').focus(); await page.keyboard.press('Enter');
  await page.locator('#workspace').waitFor();
  await expectStopped(page);
  await page.evaluate(() => fetch('/v1/dashboard/session', { method:'DELETE' }));
  await page.reload(); await page.locator('#login').waitFor(); await expectRunning(page);

  // A login configuration error and recovery do not depend on the decoration.
  await page.route('**/v1/auth/config', route => route.fulfill({ status:503, contentType:'application/json', body:'{"code":"error_generic"}' }));
  await page.reload(); await page.locator('#login-error').waitFor(); await expectRunning(page);
  await page.unroute('**/v1/auth/config'); await page.reload();
  await page.locator('#provider-login').waitFor(); await expectRunning(page);

  for (const failure of ['resource', 'canvas']) {
    const fallback = await context.newPage();
    if (failure === 'resource') await fallback.route('**/assets/login-ripple.mjs', route => route.abort());
    else await fallback.addInitScript(() => {
      const original = HTMLCanvasElement.prototype.getContext;
      HTMLCanvasElement.prototype.getContext = function (...args) {
        return this.id === 'login-ripple' ? null : original.apply(this, args);
      };
    });
    await fallback.goto(fixture.url); await fallback.locator('#provider-login').waitFor();
    assert.equal(await fallback.locator('.login-visual').evaluate(node => getComputedStyle(node).backgroundImage.includes('radial-gradient')), true);
    await fallback.locator('#provider-login').click(); await fallback.locator('#workspace').waitFor();
    await fallback.evaluate(() => fetch('/v1/dashboard/session', { method:'DELETE' }));
    await fallback.close();
  }
  assert.deepEqual(await page.evaluate(() => window.rippleCSP), []);
  assert.deepEqual(errors, []);
  console.log('PASS production handler ASCII ripples: 12 bilingual layouts, narrow-screen pause, reduced motion, visibility/page lifecycle, OAuth login stop, logout recovery, auth failure recovery, resource/Canvas fallback, keyboard and CSP');
} finally {
  await browser?.close();
  await writeFile(fixturePath + '.stop', 'stop');
  const code = await done;
  await rm(temporary, { recursive:true, force:true });
  if (code !== 0) throw new Error(output || `fixture exited ${code}`);
}
