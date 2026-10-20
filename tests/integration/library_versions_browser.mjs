// Opt-in production handlers + synthetic sources; no native Agent or model.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve, join, dirname } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const { chromium } = await import(process.env.COCONET_PLAYWRIGHT_MODULE ? pathToFileURL(resolve(process.env.COCONET_PLAYWRIGHT_MODULE)).href : 'playwright');
const temporary = await mkdtemp(join(tmpdir(), 'coconet-library-versions-'));
const file = join(temporary, 'fixture.json');
const helper = spawn('go', ['test', './apps/server', '-run', '^TestPublishedGraphBrowserHarness$', '-count=1', '-timeout=6m'], { cwd: root, stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env, COCONET_PUBLISHED_GRAPH_BROWSER_FIXTURE: file } });
let output = '', exitCode, browser;
helper.stdout.on('data', chunk => { output += chunk; }); helper.stderr.on('data', chunk => { output += chunk; });
const complete = new Promise(resolve => helper.on('exit', code => { exitCode = code; resolve(code); }));
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(check) {
  for (let i = 0; i < 600; i++) { if (exitCode !== undefined) throw new Error(output); if (await check()) return; await delay(100); }
  throw new Error('fixture timed out');
}
try {
  let fixture;
  await until(async () => { try { fixture = JSON.parse(await readFile(file, 'utf8')); return true; } catch { return false; } });
  browser = await chromium.launch({ headless: true, executablePath: process.env.COCONET_BROWSER_EXECUTABLE });
  const context = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'zh-CN', viewport: { width: 1440, height: 900 } });
  const page = await context.newPage(); page.setDefaultTimeout(15000);
  const errors = [], inputs = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => { if (request.url().endsWith('/library-selection')) inputs.push(request.postDataJSON()); });
  await page.addInitScript(() => { navigator.clipboard.writeText = async text => { window.copiedInstruction = text; }; });
  await page.goto(fixture.url + '/' + fixture.project_id);
  await page.locator('#account-key').fill(fixture.member_key); await page.locator('#login-button').click();
  await page.locator('.work-node').filter({ hasText: '补充停止条件说明' }).click();
  await page.locator('#session-action-2').click();
  await page.waitForFunction(() => document.querySelector('#session-action-2')?.getAttribute('aria-pressed') === 'true' && !document.querySelector('#session-action-2').disabled);
  assert.equal(inputs[0].source.version, 1, 'member must be able to save author v1');
  await writeFile(file + '.advance', 'advance');
  await until(async () => { try { await readFile(file + '.advance'); return false; } catch { return true; } });
  await writeFile(file + '.publish', 'publish');
  await page.locator('.work-node').filter({ hasText: '简化停止条件说明' }).waitFor();
  await page.locator('#library-open').click();
  await page.locator('.library-entry').filter({ hasText: '补充停止条件说明' }).click();
  await page.locator('#detail-content').getByRole('heading', { name: '补充停止条件说明', exact: true }).waitFor();
  const savedURL = page.url();
  await page.locator('#refresh').click();
  await page.waitForFunction(() => document.querySelector('#refresh').getAttribute('aria-busy') === 'false');
  await page.reload();
  await page.locator('#detail-content').getByRole('heading', { name: '补充停止条件说明', exact: true }).waitFor();
  assert.equal(page.url(), savedURL, 'deep link must preserve saved version');
  await page.locator('#session-action-1').click();
  await page.waitForFunction(() => window.copiedInstruction?.includes('fork_session'));
  assert.match(await page.evaluate(() => window.copiedInstruction), /"version": 1/);
  await page.keyboard.press('Escape');
  await page.locator('#detail-content').getByRole('button', { name: '查看节点最新版本' }).click();
  await page.locator('#detail-content').getByRole('heading', { name: '简化停止条件说明', exact: true }).waitFor();
  // Removing the star from live v2 must target the separately saved v1.
  await page.locator('#session-action-2').click();
  await page.waitForFunction(() => document.querySelector('#session-action-2')?.getAttribute('aria-pressed') === 'false' && !document.querySelector('#session-action-2').disabled);
  assert.equal(inputs.at(-1).remove, true); assert.equal(inputs.at(-1).source.version, 1);
  // Refreshing an unavailable saved-version link never substitutes live v2.
  await page.goto(savedURL);
  await page.waitForFunction(() => document.querySelector('#refresh').getAttribute('aria-busy') === 'false');
  assert.equal(await page.locator('#details').isVisible(), false);
  assert.deepEqual(errors, []);
  await context.close();
  console.log('PASS member collection, live continuation, fixed Library selection/deep links, refresh, Fork instruction and removal from newer graph');
} finally {
  await browser?.close(); await writeFile(file + '.stop', 'stop');
  const exited = await Promise.race([complete.then(() => true), delay(5000).then(() => false)]);
  if (!exited) helper.kill('SIGTERM');
  await complete; await rm(temporary, { recursive: true, force: true });
  if (exitCode !== 0) throw new Error(output);
}
