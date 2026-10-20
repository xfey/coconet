// Opt-in: production HTTP handlers, real Relay/checkpoint clients, synthetic
// sources only. No model or native Agent history is involved.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, writeFile, mkdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve, join, dirname } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const moduleName = process.env.COCONET_PLAYWRIGHT_MODULE;
const { chromium } = await import(moduleName ? pathToFileURL(resolve(moduleName)).href : 'playwright');
const temporary = await mkdtemp(join(tmpdir(), 'coconet-published-graph-'));
const file = join(temporary, 'fixture.json');
const helper = spawn('go', ['test', './apps/server', '-run', '^TestPublishedGraphBrowserHarness$', '-count=1', '-timeout=6m'], { cwd: root, stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env, COCONET_PUBLISHED_GRAPH_BROWSER_FIXTURE: file } });
let output = '', exitCode, browser;
helper.stdout.on('data', chunk => { output += chunk; }); helper.stderr.on('data', chunk => { output += chunk; });
const complete = new Promise(resolve => helper.on('exit', code => { exitCode = code; resolve(code); }));
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(check) { for (let i = 0; i < 600; i++) { if (exitCode !== undefined) throw new Error(output); if (await check()) return; await delay(100); } throw new Error('fixture timed out'); }
async function screenshot(page, name) {
  if (!process.env.COCONET_DASHBOARD_SCREENSHOTS) return;
  await mkdir(process.env.COCONET_DASHBOARD_SCREENSHOTS, { recursive: true });
  await page.screenshot({ path: join(process.env.COCONET_DASHBOARD_SCREENSHOTS, name + '.png'), fullPage: true });
}
async function refreshState(page, busy, { language = 'zh', reducedMotion = false } = {}) {
  await page.waitForFunction(value => document.querySelector('#refresh').getAttribute('aria-busy') === String(value), busy);
  const control = page.locator('#refresh');
  assert.equal(await control.isDisabled(), busy);
  assert.equal(await control.getAttribute('aria-label'), busy ? (language === 'zh' ? '正在刷新…' : 'Refreshing…') : (language === 'zh' ? '刷新' : 'Refresh'));
  assert.equal(await control.locator('.icon').evaluate(node => getComputedStyle(node).animationName), busy && !reducedMotion ? 'notification-spin' : 'none');
}
try {
  let fixture;
  await until(async () => { try { fixture = JSON.parse(await readFile(file, 'utf8')); return true; } catch { return false; } });
  browser = await chromium.launch({ headless: true, executablePath: process.env.COCONET_BROWSER_EXECUTABLE });
  const context = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'zh-CN', viewport: { width: 1440, height: 900 } });
  const page = await context.newPage(); page.setDefaultTimeout(15000);
  const failures = [], requests = [];
  page.on('pageerror', error => { failures.push(error.message); console.error('Browser error:', error.message); });
  page.on('request', request => requests.push(new URL(request.url()).pathname));
  await page.addInitScript(() => { navigator.clipboard.writeText = async text => { window.copiedInstruction = text; }; });
  await page.goto(fixture.url + '/' + fixture.project_id);
  await page.locator('#account-key').fill(fixture.key); await page.locator('#login-button').click();
  await page.locator('.work-node').filter({ hasText: '补充停止条件说明' }).waitFor();
  assert.equal(await page.locator('.work-node').count(), 2);
  await refreshState(page, false);
  assert.equal(new URL(page.url()).pathname, new URL(fixture.url).pathname.replace(/\/$/, '') + '/' + fixture.project_id);
  assert.equal(new URL(page.url()).hash, '');
  // Direct loading, refresh and history navigation use the same project path.
  await page.locator('.work-node').filter({ hasText: '补充停止条件说明' }).click();
  const selectedURL = page.url();
  assert.ok(new URL(selectedURL).hash.startsWith('#node='));
  await page.reload();
  await page.locator('#detail-content').getByRole('heading', { name: '补充停止条件说明', exact: true }).waitFor();
  assert.equal(page.url(), selectedURL);
  await page.goBack(); await page.locator('#details').waitFor({ state: 'hidden' });
  assert.equal(new URL(page.url()).hash, '');
  await page.goForward();
  await page.locator('#detail-content').getByRole('heading', { name: '补充停止条件说明', exact: true }).waitFor();
  await page.evaluate(() => { location.hash = ''; });
  await page.locator('#details').waitFor({ state: 'hidden' });
  // A held request gives visible feedback and blocks repeated clicks.
  let finishRefresh, refreshStarted = false;
  const refreshGate = new Promise(resolve => { finishRefresh = resolve; });
  await page.route('**/published-graph', async route => { const response = await route.fetch(); refreshStarted = true; await refreshGate; await route.fulfill({ response }); });
  await page.locator('#refresh').click(); await until(() => refreshStarted);
  await refreshState(page, true);
  const requestsWhileBusy = requests.filter(path => path.endsWith('/published-graph')).length;
  await page.locator('#refresh').evaluate(control => { control.click(); control.click(); });
  assert.equal(requests.filter(path => path.endsWith('/published-graph')).length, requestsWhileBusy);
  await screenshot(page, 'refresh-busy-desktop-zh');
  finishRefresh(); await page.unroute('**/published-graph', { behavior: 'wait' });
  await refreshState(page, false);
  assert.equal(requests.filter(path => /\/objects\/|\/dialogue$|\/projection$|\/graph$|\/library$/.test(path)).length, 0, 'published view called a legacy or body endpoint');
  await page.locator('.work-node').filter({ hasText: '补充停止条件说明' }).click();
  assert.equal(await page.locator('#detail-content img').count(), 0);
  assert.match(await page.locator('#detail-content').innerText(), /<img/);
  assert.equal(await page.evaluate(() => window.fixtureXSS), undefined);
  assert.equal(await page.locator('#view-dialogue, #library-toggle').count(), 0);
  await page.locator('#session-action-0').click();
  await page.waitForFunction(() => !!window.copiedInstruction);
  const instruction = await page.evaluate(() => window.copiedInstruction);
  assert.match(instruction, /"work_node_id": "docs"/); assert.match(instruction, /"session_id"/);
  assert.doesNotMatch(instruction, /server_session_id|fork_session/);
  await page.keyboard.press('Escape');
  assert.equal(await page.evaluate(() => document.activeElement.id), 'session-action-0');
  for (const [index, operation] of [[1, 'fork_session']]) {
    await page.locator('#session-action-' + index).click();
    await page.waitForFunction(op => window.copiedInstruction?.includes(op), operation);
    assert.match(await page.evaluate(() => window.copiedInstruction), /"work_node_id": "docs"/);
    assert.equal(await page.locator('#modal-content').evaluate(el => el.scrollWidth > el.clientWidth + 1), false);
    await screenshot(page, 'published-action-' + index + '-desktop-zh');
    await page.keyboard.press('Escape');
    assert.equal(await page.evaluate(() => document.activeElement.id), 'session-action-' + index);
  }
  await page.locator('#library-open').click();
  assert.equal(await page.locator('.library-entry').count(), 1);
  await page.locator('.library-entry').click();
  assert.match(await page.locator('#detail-content').innerText(), /固定接口行为/);
  const libraryURL = page.url();
  await page.reload();
  await page.locator('#detail-content').getByText('固定接口行为', { exact: true }).waitFor();
  assert.equal(page.url(), libraryURL);
  // Collection is a direct mutation. Failed attempts retain the same intent;
  // successful publication updates a second browser through the existing SSE.
  const peer = await context.newPage(); await peer.goto(fixture.url + '/' + fixture.project_id);
  await peer.locator('.work-node').filter({ hasText: '补充停止条件说明' }).waitFor();
  await peer.locator('#library-open').click(); assert.equal(await peer.locator('.library-entry').count(), 1);
  const intents = []; let releaseCollection, collectionAccepted = false;
  const collectionGate = new Promise(resolve => { releaseCollection = resolve; });
  await page.route('**/library-selection', async route => {
    intents.push(route.request().postDataJSON());
    if (intents.length === 1) return route.fulfill({ status: 503, json: { code: 'library_update_failed' } });
    const response = await route.fetch(); collectionAccepted = true; await collectionGate; await route.fulfill({ response });
  });
  const clipboardBeforeSave = await page.evaluate(() => window.copiedInstruction);
  await page.locator('#session-action-2').click();
  await page.getByText('收藏更新未确认，请重试。', { exact: true }).waitFor();
  assert.equal(await page.locator('#session-action-2').getAttribute('aria-pressed'), 'true');
  await page.locator('#session-action-2').click(); await until(() => collectionAccepted);
  assert.equal(await page.locator('#session-action-2').isDisabled(), true);
  await page.locator('#session-action-2').evaluate(button => { button.click(); button.click(); });
  assert.equal(intents.length, 2); assert.deepEqual(intents[0], intents[1]);
  releaseCollection(); await page.unroute('**/library-selection', { behavior: 'wait' });
  await page.waitForFunction(() => document.querySelector('#session-action-2')?.getAttribute('aria-busy') === 'false');
  assert.equal(await page.locator('#session-action-2').getAttribute('aria-pressed'), 'false');
  await peer.locator('.library-entry').waitFor({ state: 'detached' });
  assert.equal(await page.locator('#session-modal').isVisible(), false);
  assert.equal(await page.evaluate(() => window.copiedInstruction), clipboardBeforeSave);
  await screenshot(page, 'published-remove-desktop-zh');
  await page.locator('#session-action-2').click();
  await page.waitForFunction(() => document.querySelector('#session-action-2')?.getAttribute('aria-pressed') === 'true' && !document.querySelector('#session-action-2').disabled);
  await peer.locator('.library-entry').waitFor(); await peer.close();
  assert.equal(await page.locator('#session-modal').isVisible(), false);
  await screenshot(page, 'published-desktop-zh');
  // Intermediate commits do not trigger a redundant graph download. An explicit
  // refresh may still show the gap before the new checkpoint is available.
  const beforeAdvance = requests.filter(path => path.endsWith('/published-graph')).length;
  await writeFile(file + '.advance', 'advance');
  await until(async () => { try { await readFile(file + '.advance'); return false; } catch { return true; } });
  await delay(500);
  assert.equal(requests.filter(path => path.endsWith('/published-graph')).length, beforeAdvance);
  await page.locator('#refresh').click();
  await page.getByText('新的工作进展处理中...', { exact: true }).waitFor();
  assert.equal(await page.locator('.work-node').filter({ hasText: '补充停止条件说明' }).count(), 1);
  await page.locator('#notice').getByRole('button', { name: '查看同步状态', exact: true }).click();
  assert.equal(requests.filter(path => path.endsWith('/replicas')).length, 0);
  assert.match(await page.locator('#modal-content').innerText(), /会话待办/);
  await page.locator('[data-disclosure="session-items"] > summary').click();
  const followUp = page.locator('[data-disclosure^="session-follow-ups-"]');
  await followUp.locator('summary').click();
  await followUp.getByRole('button', { name: '补充停止条件说明', exact: true }).focus();
  await page.keyboard.press('Enter');
  assert.equal(await page.locator('#session-modal').evaluate(node => node.open), false);
  assert.match(await page.locator('#detail-content').innerText(), /补充停止条件说明/);
  assert.equal(await page.evaluate(() => document.activeElement.id), 'detail-close');
  await page.locator('#detail-close').click();
  await page.locator('#notice').getByRole('button', { name: '查看同步状态', exact: true }).click();
  await page.locator('[data-disclosure="session-items"] > summary').click();
  assert.match(await page.locator('#modal-content').innerText(), /后续检查大文件场景/, 'following a reference must not clear the historical item');
  await page.locator('[data-disclosure="published-sync"] summary').click();
  assert.match(await page.locator('#modal-content').innerText(), /coconet health/);
  await writeFile(file + '.publish', 'publish');
  await page.locator('#modal-content').getByText('待分析关系：1', { exact: true }).waitFor();
  assert.equal(await page.locator('[data-disclosure="published-sync"]').evaluate(el => el.open), true, 'checkpoint update collapsed sync details');
  assert.equal(await page.locator('[data-disclosure="published-sync"] summary').evaluate(el => el === document.activeElement), true, 'checkpoint update lost keyboard focus');
  await page.keyboard.press('Escape');
  await page.locator('.work-node').filter({ hasText: '简化停止条件说明' }).waitFor();
  await page.getByText('节点关系待分析', { exact: true }).waitFor();
  await refreshState(page, false); // Pending device work must not spin or lock refresh.
  await page.locator('#notice').getByRole('button', { name: '查看同步状态', exact: true }).click();
  assert.equal(await page.locator('#modal-content').getByText('下一步', { exact: true }).count(), 0);
  await page.locator('[data-disclosure="session-items"] > summary').click();
  assert.match(await page.locator('#modal-content').innerText(), /后续检查大文件场景/);
  const diagnosticCommand = 'coconet inference status\ncoconet health --json\ncoconet diagnostics list';
  assert.equal(await page.locator('[data-disclosure="published-sync"]').evaluate(el => el.open), false);
  assert.equal(await page.locator('#modal-content .command-block button').isVisible(), false);
  await page.locator('[data-disclosure="published-sync"] summary').focus();
  await page.keyboard.press('Enter');
  await page.locator('#modal-content .command-block button').click();
  assert.equal(await page.evaluate(() => window.copiedInstruction), diagnosticCommand);
  assert.equal(await page.locator('[data-disclosure="published-sync"]').evaluate(el => el.open), true);
  await screenshot(page, 'published-status-zh');
  await page.keyboard.press('Tab'); assert.equal(await page.locator('#session-modal').evaluate(el => el.contains(document.activeElement)), true);
  await page.keyboard.press('Escape');
  assert.equal(await page.locator('#notice button').evaluate(el => el === document.activeElement), true);
  // Retain the last valid graph on corruption, then recover on explicit retry.
  let corrupt = true;
  await page.route('**/published-graph', async route => { const headers = { ...route.request().headers() }; delete headers['if-none-match']; const response = await route.fetch({ headers }); const data = await response.json(); if (corrupt) data.relay.checkpoint.checkpoint.sha256 = '0'.repeat(64); await route.fulfill({ response, json: data }); });
  await page.locator('#refresh').click();
  await page.getByText('工作图加载异常，请刷新重试。', { exact: true }).waitFor();
  await refreshState(page, false);
  assert.equal(await page.locator('.work-node').count(), 2);
  await page.locator('#notice').getByRole('button', { name: '查看同步状态', exact: true }).click();
  assert.match(await page.locator('#modal-content').innerText(), /上次同步的结果/);
  assert.match(await page.locator('#modal-content').innerText(), /工作图加载异常/);
  await page.keyboard.press('Escape');
  corrupt = false; await page.locator('#refresh').click();
  await page.getByText('节点关系待分析', { exact: true }).waitFor();
  await refreshState(page, false);
  await page.locator('#notice').getByRole('button', { name: '查看同步状态', exact: true }).click();
  assert.doesNotMatch(await page.locator('#modal-content').innerText(), /上次同步的结果|工作图加载异常/);
  await page.keyboard.press('Escape');
  // No periodic graph download while the project is idle.
  const count = requests.filter(path => path.endsWith('/published-graph')).length;
  await delay(1200); assert.equal(requests.filter(path => path.endsWith('/published-graph')).length, count);
  await page.unroute('**/published-graph');
  let release, releaseNext, started = false, nextStarted = false;
  const gate = new Promise(resolve => { release = resolve; });
  const nextGate = new Promise(resolve => { releaseNext = resolve; });
  const pattern = '**/projects/' + fixture.project_id + '/published-graph';
  const nextPattern = '**/projects/' + fixture.empty_project_id + '/published-graph';
  await page.route(pattern, async route => { const response = await route.fetch(); started = true; await gate; await route.fulfill({ response }); });
  await page.route(nextPattern, async route => { const response = await route.fetch(); nextStarted = true; await nextGate; await route.fulfill({ response }); });
  await page.locator('#refresh').click(); await until(() => started);
  await refreshState(page, true);
  await page.locator('.project-item[data-project-id="' + fixture.empty_project_id + '"]').click();
  await until(() => nextStarted);
  release(); await page.unroute(pattern, { behavior: 'wait' });
  await refreshState(page, true); // An old response cannot unlock the new request.
  releaseNext(); await page.unroute(nextPattern, { behavior: 'wait' });
  await refreshState(page, false);
  assert.equal(await page.locator('.work-node').count(), 0, 'late checkpoint restored the previous project');
  assert.match(await page.locator('#graph-empty').innerText(), /暂无内容/);
  assert.equal(await page.locator('#notice').isVisible(), false);
  await page.locator('#canvas-settings').click();
  assert.equal(await page.locator('#modal-content').getByRole('button', { name: '查看同步状态', exact: true }).count(), 0);
  await page.keyboard.press('Escape');
  assert.equal(await page.evaluate(() => document.activeElement.id), 'canvas-settings');
  await page.locator('.project-item[data-project-id="' + fixture.project_id + '"]').click();
  await page.locator('.work-node').filter({ hasText: '简化停止条件说明' }).waitFor();
  await page.goBack(); await refreshState(page, false);
  await page.waitForFunction(id => document.querySelector('.project-item[aria-current="page"]')?.dataset.projectId === id, fixture.empty_project_id);
  assert.equal(await page.locator('.work-node').count(), 0);
  await page.goForward();
  await page.locator('.work-node').filter({ hasText: '简化停止条件说明' }).waitFor();
  assert.equal(new URL(page.url()).pathname, new URL(fixture.url).pathname.replace(/\/$/, '') + '/' + fixture.project_id);
  assert.equal(await page.locator('#install-open').count(), 0);
  await page.locator('#project-share').click();
  await page.getByRole('button', { name: '连接本地文件夹', exact: true }).click();
  await page.locator('[data-disclosure="sharing-scope"] summary').click();
  assert.doesNotMatch(await page.locator('#modal-content').innerText(), /加密|模型额度|模型用量|默认模型|支持 macOS/);
  assert.match(await page.locator('#modal-content').innerText(), /已有历史不会自动导入|工具调用/);
  assert.equal(await page.locator('#modal-content button.primary').count(), 0, 'installation copy controls must stay secondary');
  await screenshot(page, 'published-install-desktop-zh');
  await page.keyboard.press('Escape');
  assert.equal(await page.evaluate(() => document.activeElement.id), 'project-share');
  // The status dialog needs no remote device report, even when opened again.
  await page.locator('#notice').getByRole('button', { name: '查看同步状态', exact: true }).click();
  assert.equal(requests.filter(path => path.endsWith('/replicas')).length, 0);
  await page.keyboard.press('Escape');
  const storage = await context.storageState();
  for (const locale of ['zh-CN', 'en-US']) {
    const mobile = await browser.newContext({ ignoreHTTPSErrors: true, locale, storageState: storage, viewport: { width: 390, height: 844 } });
    const p = await mobile.newPage(); p.on('pageerror', error => failures.push(error.message));
    await p.goto(fixture.url + '/' + fixture.project_id);
    await p.locator('.work-node').filter({ hasText: '简化停止条件说明' }).waitFor();
    const lang = locale === 'zh-CN' ? 'zh' : 'en';
    await refreshState(p, false, { language: lang });
    let releaseMobile, mobileStarted = false;
    const mobileGate = new Promise(resolve => { releaseMobile = resolve; });
    await p.route('**/published-graph', async route => { const response = await route.fetch(); mobileStarted = true; await mobileGate; await route.fulfill({ response }); });
    await p.locator('#refresh').click(); await until(() => mobileStarted);
    await refreshState(p, true, { language: lang });
    await screenshot(p, 'refresh-busy-' + locale);
    await p.emulateMedia({ reducedMotion: 'reduce' });
    await refreshState(p, true, { language: lang, reducedMotion: true });
    releaseMobile(); await p.unroute('**/published-graph', { behavior: 'wait' });
    await refreshState(p, false, { language: lang, reducedMotion: true });
    await p.emulateMedia({ reducedMotion: 'no-preference' });
    await p.locator('.work-node').filter({ hasText: '简化停止条件说明' }).click();
    await screenshot(p, 'published-mobile-' + locale);
    for (const index of [1]) {
      await p.locator('#session-action-' + index).click();
      for (const width of [390, 320]) {
        await p.setViewportSize({ width, height: 844 });
        assert.equal(await p.locator('#modal-content').evaluate(el => el.scrollWidth > el.clientWidth + 1), false, 'action modal overflow');
        await screenshot(p, `published-action-${index}-${locale}-${width}`);
      }
      await p.keyboard.press('Escape');
      assert.equal(await p.evaluate(() => document.activeElement.id), 'session-action-' + index);
    }

    await p.locator('#detail-close').click();
    await p.locator('#library-open').click();
    await p.locator('.library-entry').click();
    await p.locator('#session-action-2').click();
    await p.waitForFunction(() => document.querySelector('#session-action-2')?.getAttribute('aria-pressed') === 'false' && !document.querySelector('#session-action-2').disabled);
    assert.equal(await p.locator('#session-modal').isVisible(), false);
    for (const width of [390, 320]) {
      await p.setViewportSize({ width, height: 844 });
      assert.equal(await p.locator('#detail-content').evaluate(el => el.scrollWidth > el.clientWidth + 1), false, 'Collection control overflow');
      await screenshot(p, `published-remove-${locale}-${width}`);
    }
    await p.locator('#session-action-2').click();
    await p.waitForFunction(() => document.querySelector('#session-action-2')?.getAttribute('aria-pressed') === 'true' && !document.querySelector('#session-action-2').disabled);
    assert.equal(await p.locator('#notice').evaluate(el => { const r=el.getBoundingClientRect(); return r.right > innerWidth || r.bottom > innerHeight; }), false, 'mobile notice escaped viewport');
    await p.locator('#detail-close').click();
    assert.equal(await p.locator('#canvas-sync-status').count(), 0);
    await p.locator('#notice').getByRole('button', { name: locale === 'en-US' ? 'View sync status' : '查看同步状态', exact: true }).click();
    await p.locator('[data-disclosure="published-sync"] summary').click();
    await p.locator('[data-disclosure="session-items"] > summary').click();
    await p.locator('[data-disclosure^="session-follow-ups-"] > summary').click();
    assert.match(await p.locator('#modal-content').innerText(), locale === 'en-US' ? /Conversation notes/ : /会话待办/);
    assert.equal(await p.locator('.session-follow-up').count(), 1);
    for (const width of [1440, 390, 320]) {
      await p.setViewportSize({ width, height: 844 });
      await p.locator('#modal-content').waitFor();
      const overflow = await p.locator('#modal-content').evaluate(el => el.scrollWidth > el.clientWidth + 1);
      assert.equal(overflow, false, locale + ' modal overflow at ' + width);
      await screenshot(p, `published-status-${locale}-${width}`);
    }
    await p.keyboard.press('Escape');
    assert.equal(await p.locator('#install-open').count(), 0);
    await p.locator('#project-share').click();
    await p.getByRole('button', { name: locale === 'en-US' ? 'Connect a local folder' : '连接本地文件夹', exact: true }).click();
    assert.equal(await p.locator('#modal-title').innerText(), locale === 'en-US' ? 'Connect a local folder' : '连接本地文件夹');
    const scope = p.locator('[data-disclosure="sharing-scope"]');
    assert.equal(await scope.evaluate(el => el.open), false);
    assert.equal(await p.locator('.installation-guide .command-block').count(), 2);
    assert.equal(await p.locator('.installation-guide .command-block:visible').count(), 1);
    assert.equal(await p.locator('[data-disclosure="first-use"]').evaluate(el => el.open), false);
    assert.doesNotMatch(await p.locator('.installation-guide').innerText(), /npm install/);
    assert.doesNotMatch(await p.locator('.installation-guide').innerText(), /连接到：|Connect to:/);
    assert.match(await p.locator('.installation-guide').innerText(), locale === 'en-US' ? /Confirm the connection in your browser/ : /按浏览器提示确认连接/);
    await screenshot(p, `published-install-compact-${locale}`);
    await p.locator('[data-disclosure="first-use"] summary').focus();
    await p.keyboard.press('Enter');
    assert.equal(await p.locator('.installation-guide .command-block:visible').count(), 2);
    assert.match(await p.locator('[data-disclosure="first-use"]').innerText(), locale === 'en-US' ? /If your Agent asks for Hook permission/ : /如 Agent 提示允许 Hook/);
    await p.locator('[data-disclosure="sharing-scope"] summary').click();
    assert.match(await scope.innerText(), /已有历史不会自动导入|Existing history is not imported automatically/);
    assert.doesNotMatch(await p.locator('#modal-content').innerText(), /加密|模型用量|模型额度|默认模型|encryption|model usage|default model|Requires macOS/i);
    assert.match(await p.locator('#modal-content').innerText(), /工具调用|tool calls/);
    for (const width of [1440, 390, 320]) {
      await p.setViewportSize({width, height:844});
      assert.equal(await p.locator('#modal-content').evaluate(el => el.scrollWidth > el.clientWidth + 1), false, 'installation modal overflow');
      await screenshot(p, `published-install-${locale}-${width}`);
    }
    await p.keyboard.press('Escape');
    assert.equal(await p.evaluate(() => document.activeElement.id), 'project-share');
    await mobile.close();
  }
  assert.deepEqual(failures, []);
  console.log('PASS hosted client checkpoint graph, manual lag/pending states, checkpoint-only SSE updates, fixed-source instruction, Library read, XSS literal text, corrupt-response recovery, late response isolation, no body/model endpoints, bilingual 1440/390/320 layouts and keyboard dialog behavior');
} finally {
  await browser?.close(); await writeFile(file + '.stop', 'stop');
  await Promise.race([complete, delay(10000)]);
  if (exitCode === undefined) helper.kill('SIGTERM');
  if (exitCode !== 0) console.error(output);
  await rm(temporary, { recursive: true, force: true });
}
