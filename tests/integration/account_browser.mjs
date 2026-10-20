import assert from 'node:assert/strict';
import { createHash, randomBytes } from 'node:crypto';
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, writeFile, rm, mkdir, realpath } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const { chromium } = await import(pathToFileURL(resolve(process.env.COCONET_PLAYWRIGHT_MODULE)).href);
const temporary = await mkdtemp(join(tmpdir(), 'coconet-account-browser-'));
const file = join(temporary, 'fixture.json');
const helper = spawn('go', ['test', './apps/server', '-run', '^TestUnifiedLoginBrowserHarness$', '-count=1', '-timeout=4m'], { env: { ...process.env, COCONET_ACCOUNT_BROWSER_FIXTURE: file }, stdio: ['ignore', 'pipe', 'pipe'] });
let output = '', exitCode, browser, cli;
helper.stdout.on('data', chunk => { output += chunk; }); helper.stderr.on('data', chunk => { output += chunk; });
const done = new Promise(resolve => helper.on('exit', code => { exitCode = code; resolve(code); }));
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function screenshot(page, name) {
  const directory = process.env.COCONET_ACCOUNT_SCREENSHOTS;
  if (!directory) return;
  await mkdir(directory, { recursive: true });
  await page.screenshot({ path: join(directory, name + '.png') });
}
try {
  let fixture;
  for (let i = 0; i < 300; i++) {
    if (exitCode !== undefined) throw new Error(output);
    try { fixture = JSON.parse(await readFile(file, 'utf8')); break; } catch { await delay(100); }
  }
  assert.ok(fixture, 'fixture did not start');
  browser = await chromium.launch({ headless: true, executablePath: process.env.COCONET_BROWSER_EXECUTABLE });
  const context = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'en-US' });
  const page = await context.newPage(); const errors = [];
  await page.addInitScript(() => { navigator.clipboard.writeText = async text => { window.fixtureClipboard = text; }; });
  page.on('pageerror', error => errors.push(error.message));
  // Follow the old API-host link, including its fragment, onto the browser origin.
  await page.goto(fixture.cli_url + '/#auth_error=login_failed');
  await page.getByRole('button', { name: 'Continue with GitHub' }).waitFor();
  assert.equal(page.url(), fixture.url + '/#auth_error=login_failed');
  await page.goto(fixture.url + '/');
  await page.getByRole('button', { name: 'Continue with GitHub' }).waitFor();
  for (const width of [1440, 1024, 390, 320]) {
    await page.setViewportSize({ width, height: 900 });
    for (const language of ['en', 'zh']) {
      await page.locator('#login [data-language]').selectOption(language);
      const layout = await page.evaluate(() => {
        const panel = document.querySelector('.login-panel').getBoundingClientRect();
        const button = document.querySelector('#provider-login').getBoundingClientRect();
        return { overflow: document.documentElement.scrollWidth > innerWidth, right: panel.right, x: panel.x, buttonWidth: button.width, buttonInside: button.left >= panel.left && button.right <= panel.right };
      });
      assert.equal(await page.locator('#provider-login > svg').count(), 1);
      assert.equal(await page.locator('#debug-login').count(), 0);
      assert.equal(await page.locator('#provider-login').evaluate(node => node.getBoundingClientRect().height), 40);
      assert.equal(layout.overflow, false);
      assert.equal(layout.right, width);
      assert.equal(layout.buttonInside, true);
      assert.ok(layout.buttonWidth >= 240);
      assert.equal(layout.x > 0, width > 760);
      assert.equal(await page.locator('#login-form').isVisible(), false);
      assert.equal(await page.locator('#login-title').innerText(), language === 'zh' ? '打开项目' : 'Open your projects');
      await screenshot(page, 'login-' + width + '-' + language);
      assert.equal(await page.locator('#login-install').count(), 0);
      assert.doesNotMatch(await page.locator('#login').innerText(), /连接项目目录|Connect a project folder/);
    }
  }
  async function chooseEntry(target, kind) {
    await target.locator('.project-entry > .project-entry-choices [data-choice="' + kind + '"]').click();
    await target.waitForFunction(() => [...document.querySelectorAll('.project-entry')].every(node => node.getAnimations({subtree:true}).every(a => a.playState !== 'running')));
  }
  async function checkProjectChoices(target, width, folder) {
    const root = target.locator('.project-entry');
    assert.equal(await root.locator(':scope > .project-entry-choices > button').count(), folder ? 3 : 2);
    assert.equal(await target.locator('.connection-folder').count(), folder ? 1 : 0);
    assert.equal(await target.getByRole('button', { name: /返回选项|All options/ }).count(), 0);
    for (const kind of folder ? ['existing', 'join', 'create'] : ['join', 'create']) {
      if (await root.locator('[data-choice="' + kind + '"]').isDisabled()) continue;
      await chooseEntry(target, kind);
      assert.equal(await root.locator('form:visible').count(), 1);
      const layout = await root.evaluate(node => {
        const nav = node.querySelector('.project-entry-choices').getBoundingClientRect();
        const form = node.querySelector('.project-entry-forms').getBoundingClientRect();
        const action = node.querySelector('form:not([hidden]) .ui-button');
        return { width:node.clientWidth, side:form.left >= nav.right, below:form.top >= nav.bottom,
          buttonHeight:action.getBoundingClientRect().height, icons:action.querySelectorAll('svg').length,
          overflow:node.scrollWidth > node.clientWidth + 1 };
      });
      assert.equal(layout.width < 420 ? layout.below : layout.side, true);
      assert.equal(layout.buttonHeight, 40); assert.equal(layout.icons, 1); assert.equal(layout.overflow, false);
      assert.equal(await target.locator('#modal-content').evaluate(el => el.scrollWidth > el.clientWidth + 1), false);
    }
  }
  async function checkProjectNotice(target, language, joined, name = '') {
    const title = joined ? (language === 'zh' ? '已加入「' + name + '」' : 'Joined ' + name)
      : language === 'zh' ? '你受邀加入一个协作项目' : "You're invited to join a collaborative project";
    await target.getByRole('heading', { name: title, exact: true }).waitFor();
    assert.equal(await target.locator('.project-entry,.installation-guide').count(), 0);
    assert.equal(await target.locator('#modal-content input').count(), 0);
    if (!joined) {
      await target.locator('.project-invitation strong').waitFor();
      assert.equal(await target.locator('.project-invitation strong').textContent(), name);
      assert.equal(await target.locator('.project-invitation .dialog-description:visible').textContent(), language === 'zh'
        ? '加入后，可以查看项目协作图，了解同事的工作进展和共享会话。'
        : 'Join to view the project’s work graph and explore your teammates’ progress and shared sessions.');
    }
    assert.deepEqual(await target.locator('#modal-content button:visible').allTextContents(), joined
      ? language === 'zh' ? ['好的', '如何连接项目目录'] : ['OK', 'How to connect a project folder']
      : [language === 'zh' ? '加入项目' : 'Join project']);
    if (joined) assert.equal(await target.locator('#modal-content .dialog-description').textContent(), language === 'zh'
      ? '如需同步自己的工作进展，可以连接项目目录。'
      : 'To sync your own work progress, connect a project folder.');
    for (const width of [1280, 390, 320]) {
      await target.setViewportSize({ width, height: 844 });
      assert.equal(await target.locator('#modal-content').evaluate(node => node.scrollWidth > node.clientWidth + 1), false);
      assert.equal(await target.locator('#session-modal').evaluate(node => {
        const bounds = node.getBoundingClientRect(), close = node.querySelector('#modal-close').getBoundingClientRect();
        return bounds.left >= 0 && bounds.right <= innerWidth && close.right <= bounds.right
          && [...node.querySelectorAll('.ui-button:not([hidden])')].every(button => {
            const b = button.getBoundingClientRect(); return b.left >= bounds.left && b.right <= bounds.right && b.height >= 40;
          });
      }), true, 'Invitation and success actions must fit the viewport, including long names');
      await screenshot(target, (joined ? 'project-joined-' : 'project-invitation-') + width + '-' + language);
    }
  }
  const memberAccounts = [];
  const memberProjectName = 'Member collaboration ' + 'x'.repeat(70);
  let sharedInvitation;
  for (const id of ['member-a', 'member-b']) {
    const isolated = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'en-US' });
    const trial = await isolated.newPage(); trial.on('pageerror', error => errors.push(error.message));
    const verifier = randomBytes(32).toString('base64url');
    const start = id === 'member-a' ? await (await isolated.request.post(fixture.url + '/v1/auth/device', { data: {
      device_name: 'Member device', code_challenge: createHash('sha256').update(verifier).digest('base64url'),
    } })).json() : null;
    if (!start) await trial.route('**/v1/dashboard/projects/*/preview', route => route.fulfill({ status: 503, json: { code: 'temporarily_unavailable' } }));
    await trial.goto(start ? start.verification_uri : sharedInvitation);
    await isolated.addCookies([{ name: 'fixture_identity', value: id, url: fixture.url, secure: true, httpOnly: true }]);
    await trial.getByRole('button', { name: 'Continue with GitHub' }).click();
    await trial.locator('#workspace').waitFor();
    assert.equal(await trial.locator('#account-label').innerText(), id);
    const account = await (await isolated.request.get(fixture.url + '/v1/dashboard/session')).json();
    memberAccounts.push(account.account.account_id);
    if (start) {
      await trial.getByRole('button', { name: 'Confirm connection', exact: true }).click();
      await trial.locator('#session-modal').waitFor({ state: 'hidden' });
      const token = await (await isolated.request.post(fixture.url + '/v1/auth/token', { data: { device_code: start.device_code, code_verifier: verifier } })).json();
      assert.equal(token.status, 'ok'); assert.equal(token.account.account_id, account.account.account_id);
      await trial.locator('#project-manage').click();
      await checkProjectChoices(trial, 1280, false);
      await trial.getByLabel('New project name', { exact: true }).fill(memberProjectName);
      // Closing during post-creation navigation must not reopen the folder guide.
      let releaseCreatedProjects, createdProjectsStarted;
      const createdProjectsGate = new Promise(resolve => { releaseCreatedProjects = resolve; });
      const createdProjectsPending = new Promise(resolve => { createdProjectsStarted = resolve; });
      await trial.route('**/v1/dashboard/projects', async route => {
        if (route.request().method() === 'GET') { createdProjectsStarted(); await createdProjectsGate; }
        await route.continue();
      });
      await trial.getByRole('button', { name: 'Create project', exact: true }).click();
      await createdProjectsPending;
      await trial.keyboard.press('Escape');
      await trial.locator('#project-manage').click();
      releaseCreatedProjects();
      await trial.unrouteAll({ behavior: 'wait' });
      await trial.getByRole('button', { name: memberProjectName, exact: true }).waitFor();
      assert.equal(await trial.locator('.project-entry').isVisible(), true);
      assert.equal(await trial.locator('.installation-guide').count(), 0);
      await trial.keyboard.press('Escape');
      await trial.locator('#project-share').click();
      await trial.getByRole('button', { name: 'Copy project link', exact: true }).waitFor();
      sharedInvitation = await trial.locator('.share-result .copy-field pre').textContent();
      await trial.locator('#modal-close').click();
    } else {
      await trial.getByText('Could not load project details. Please retry.', { exact: true }).waitFor();
      assert.equal(await trial.getByRole('button', { name: 'Join project', exact: true }).isDisabled(), true);
      await trial.unroute('**/v1/dashboard/projects/*/preview');
      for (const [code, status, message] of [
        ['project_not_found', 404, 'This project does not exist or is no longer accessible.'],
        ['project_archived', 409, 'This project is archived and cannot be joined.'],
      ]) {
        await trial.route('**/v1/dashboard/projects/*/preview', route => route.fulfill({ status, json: { code } }));
        await trial.getByRole('button', { name: 'Retry', exact: true }).click();
        await trial.getByText(message, { exact: true }).waitFor();
        assert.equal(await trial.getByRole('button', { name: 'Join project', exact: true }).isDisabled(), true);
        await trial.unroute('**/v1/dashboard/projects/*/preview');
      }
      // A delayed preview must not overwrite another dialog after being closed.
      let releasePreview, previewStarted;
      const previewGate = new Promise(resolve => { releasePreview = resolve; });
      const previewPending = new Promise(resolve => { previewStarted = resolve; });
      await trial.route('**/v1/dashboard/projects/*/preview', async route => { previewStarted(); await previewGate; await route.continue(); });
      await trial.getByRole('button', { name: 'Retry', exact: true }).click();
      await previewPending;
      await trial.getByText('Loading project details…', { exact: true }).waitFor();
      assert.equal(await trial.getByRole('button', { name: 'Join project', exact: true }).isDisabled(), true);
      await screenshot(trial, 'project-invitation-loading');
      await trial.keyboard.press('Escape');
      await trial.locator('#project-manage').click();
      releasePreview();
      await trial.unrouteAll({ behavior: 'wait' });
      assert.equal(await trial.locator('.project-entry').isVisible(), true);
      assert.equal(await trial.locator('.project-invitation').count(), 0);
      await trial.reload();
      await checkProjectNotice(trial, 'en', false, memberProjectName);
      for (const language of ['zh', 'en']) {
        await trial.locator('#login [data-language]').evaluate((select, value) => {
          select.value = value; select.dispatchEvent(new Event('change', { bubbles: true }));
        }, language);
        await checkProjectNotice(trial, language, false, memberProjectName);
      }
      assert.equal((await (await isolated.request.get(fixture.url + '/v1/dashboard/projects')).json()).projects.length, 0);
      await trial.route('**/v1/dashboard/connections', route => route.fulfill({ status: 503, json: { code: 'temporarily_unavailable' } }));
      await trial.getByRole('button', { name: 'Join project', exact: true }).click();
      await trial.locator('.account-error').filter({ hasText: /.+/ }).waitFor();
      assert.equal(await trial.getByRole('button', { name: 'Join project', exact: true }).isEnabled(), true);
      assert.equal((await (await isolated.request.get(fixture.url + '/v1/dashboard/projects')).json()).projects.length, 0);
      await trial.unroute('**/v1/dashboard/connections');
      // Closing while the joined project's canvas loads must not later reopen
      // a success screen or replace a newly opened dialog with instructions.
      let releaseProjectList, projectListStarted;
      const projectListGate = new Promise(resolve => { releaseProjectList = resolve; });
      const projectListPending = new Promise(resolve => { projectListStarted = resolve; });
      await trial.route('**/v1/dashboard/projects', async route => { projectListStarted(); await projectListGate; await route.continue(); });
      await trial.getByRole('button', { name: 'Join project', exact: true }).click();
      await projectListPending;
      assert.equal(await trial.getByRole('button', { name: 'Join project', exact: true }).isDisabled(), true);
      assert.equal(await trial.getByRole('button', { name: 'Join project', exact: true }).getAttribute('aria-busy'), 'true');
      await trial.keyboard.press('Escape');
      await trial.locator('#session-modal').waitFor({ state: 'hidden' });
      await trial.locator('#project-manage').click();
      releaseProjectList();
      await trial.unrouteAll({ behavior: 'wait' });
      await trial.getByRole('button', { name: memberProjectName, exact: true }).waitFor();
      assert.equal(await trial.locator('.project-entry').isVisible(), true);
      assert.equal(await trial.locator('.installation-guide').count(), 0);
      assert.equal(await trial.locator('.account-error').textContent(), '');
      await chooseEntry(trial, 'join');
      await trial.getByRole('textbox', { name: 'Join with a shared link', exact: true }).fill(sharedInvitation);
      await trial.evaluate(() => { window.invitationCloseCount = 0; document.querySelector('#session-modal').addEventListener('close', () => window.invitationCloseCount++); });
      await trial.getByRole('button', { name: 'Join project', exact: true }).click();
      await checkProjectNotice(trial, 'en', true, memberProjectName);
      assert.equal(await trial.evaluate(() => window.invitationCloseCount), 0, 'Success stays in the same open dialog');
      assert.equal(await trial.locator('.account-error').textContent(), '');
      assert.equal(await trial.getByRole('button', { name: 'OK', exact: true }).evaluate(node => node === document.activeElement), true);
      await trial.keyboard.press('Tab');
      assert.equal(await trial.getByRole('button', { name: 'How to connect a project folder', exact: true }).evaluate(node => node === document.activeElement), true);
      await trial.keyboard.press('Shift+Tab');
      await trial.keyboard.press('Enter');
      await trial.locator('#session-modal').waitFor({ state: 'hidden' });
      assert.equal(await trial.locator('#project-manage').evaluate(node => node === document.activeElement), true);
      assert.equal(await trial.locator('.installation-guide').count(), 0);
      assert.equal((await (await isolated.request.get(fixture.url + '/v1/dashboard/projects')).json()).projects.length, 1);

    }
    await trial.reload(); await trial.locator('#workspace').waitFor();
    assert.equal(await trial.locator('#account-label').innerText(), id);
    await trial.getByRole('button', { name: memberProjectName, exact: true }).waitFor();
    assert.equal(await trial.locator('#session-modal').isVisible(), false, 'Existing members go straight to the project');
    await isolated.close();
  }
  assert.notEqual(memberAccounts[0], memberAccounts[1]);
  // A new account can connect directly to another account's project from an
  // empty project list. Preview and failed approvals must not join it.
  const newcomer = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'en-US' });
  const newcomerPage = await newcomer.newPage(); newcomerPage.on('pageerror', error => errors.push(error.message));
  const newcomerVerifier = randomBytes(32).toString('base64url');
  const newcomerStart = await (await newcomer.request.post(fixture.url + '/v1/auth/device', { data: {
    device_name: 'New teammate', code_challenge: createHash('sha256').update(newcomerVerifier).digest('base64url'), connect_project: true,
    directory: { cwd: '/work/new-teammate', path: '/work/new-teammate', revision: 1, remote: true, can_browse: false },
  } })).json();
  await newcomerPage.goto(newcomerStart.verification_uri);
  await newcomer.addCookies([{ name: 'fixture_identity', value: 'newcomer', url: fixture.url, secure: true, httpOnly: true }]);
  await newcomerPage.getByRole('button', { name: 'Continue with GitHub' }).click();
  await newcomerPage.locator('#workspace').waitFor();
  assert.equal(await newcomerPage.locator('[data-choice=existing]').isDisabled(), true);
  await chooseEntry(newcomerPage, 'join');
  const newcomerInput = newcomerPage.getByRole('textbox', { name: 'Join with a shared link', exact: true });
  await newcomerInput.waitFor();
  const memberships = async () => (await (await newcomer.request.get(fixture.url + '/v1/dashboard/projects')).json()).projects;
  assert.equal((await memberships()).length, 0);
  await newcomerInput.fill('not-a-project');
  await newcomerPage.getByRole('button', { name: 'Join project', exact: true }).click();
  await newcomerPage.getByText('Invalid shared link. Copy the full link from Share.', { exact: true }).waitFor();
  await newcomerInput.fill(sharedInvitation.replace(fixture.url, 'https://another.example'));
  await newcomerPage.getByRole('button', { name: 'Join project', exact: true }).click();
  await newcomerPage.getByText('Enter a project link from this server.', { exact: true }).waitFor();
  await newcomerInput.fill(sharedInvitation);
  await chooseEntry(newcomerPage, 'create');
  await newcomerPage.getByLabel('New project name', { exact: true }).fill('Must not create while joining');
  await chooseEntry(newcomerPage, 'join');
  assert.equal(await newcomerInput.inputValue(), sharedInvitation);
  let finishJoin;
  const joinGate = new Promise(resolve => { finishJoin = resolve; });
  await newcomerPage.route('**/v1/dashboard/authorize', async route => { await joinGate; await route.fulfill({ status: 503, json: { code: 'temporarily_unavailable' } }); });
  await newcomerInput.press('Enter');
  assert.equal(await newcomerPage.locator('.project-entry-form button').evaluateAll(nodes => nodes.every(node => node.disabled)), true);
  await newcomerPage.locator('[data-form=create] button').evaluate(node => node.click());
  finishJoin();
  await newcomerPage.getByText('Could not complete the action. Check your connection and access, then retry.', { exact: true }).waitFor();
  assert.equal((await memberships()).length, 0);
  assert.equal(await newcomerInput.isDisabled(), false);
  await newcomerPage.unroute('**/v1/dashboard/authorize');
  await newcomerInput.press('Enter');
  await newcomerPage.locator('#session-modal').waitFor({ state: 'hidden' });
  assert.equal((await memberships()).length, 1);
  const newcomerGrant = await (await newcomer.request.post(fixture.url + '/v1/auth/token', { data: { device_code: newcomerStart.device_code, code_verifier: newcomerVerifier } })).json();
  assert.equal(newcomerGrant.status, 'ok'); assert.equal(newcomerGrant.project_id, new URL(sharedInvitation).pathname.slice(1));
  await newcomer.close();

  await page.setViewportSize({ width: 1280, height: 900 });
  await page.locator('#login [data-language]').selectOption('en');
  await page.getByRole('button', { name: 'Continue with GitHub' }).click();
  await page.locator('#workspace').waitFor();
  assert.equal(await page.locator('#account-name').textContent(), 'Browser developer');
  await page.locator('#project-list.projects-empty').waitFor();
  assert.equal(await page.locator('#notice').isVisible(), false);
  assert.equal(await page.locator('#graph-empty').isVisible(), false);
  assert.equal(await page.locator('#library-open').isVisible(), false);
  assert.equal(await page.locator('#project-share').isDisabled(), true);
  assert.equal(await page.locator('#canvas-sync-status').count(), 0);
  assert.match(await page.locator('#project-list').textContent(), /Create a project or join/);
  assert.equal(await page.locator('#identity #logout').count(), 0);
  assert.equal(await page.locator('#account-name svg').count(), 1);
  await page.locator('#canvas-settings').click();
  assert.equal(await page.locator('.project-settings').count(), 0);
  await page.locator('#modal-close').click();
  let unauthorizedDirectoryRequests = 0, creationJoins = 0;
  const trackDirectory = request => {
    if (request.url().endsWith('/v1/dashboard/authorize')) unauthorizedDirectoryRequests++;
    if (request.url().endsWith('/v1/dashboard/connections')) creationJoins++;
  };
  page.on('request', trackDirectory);
  await page.locator('#project-manage').click();
  assert.equal(await page.getByRole('button', { name: 'Leave project', exact: true }).count(), 0);
  await chooseEntry(page, 'create');
  await page.getByLabel('New project name', { exact: true }).fill('Shared repository');
  const focusedField = await page.getByLabel('New project name', { exact: true }).evaluate(node => {
    const style = getComputedStyle(node);
    return { outline: style.outlineStyle, border: style.borderTopColor, width: style.borderTopWidth };
  });
  assert.deepEqual(focusedField, { outline: 'none', border: 'rgb(36, 36, 36)', width: '1px' });
  await page.keyboard.press('Tab');
  assert.equal(await page.getByRole('button', { name: 'Create project', exact: true }).evaluate(node => {
    const style = getComputedStyle(node);
    return node === document.activeElement && style.outlineStyle === 'solid' && style.outlineOffset === '-2px' && style.outlineColor === 'rgb(255, 255, 255)';
  }), true, 'Primary action must retain a visible inset keyboard cue');
  assert.equal(await page.getByRole('textbox', { name: 'Join with a shared link', exact: true }).isVisible(), false);
  assert.equal(await page.getByLabel('New project name', { exact: true }).isVisible(), true);
  assert.equal(await page.getByLabel('New project name', { exact: true }).inputValue(), 'Shared repository');
  const createRequests = [];
  let releaseCreate;
  const createGate = new Promise(resolve => { releaseCreate = resolve; });
  await page.route('**/v1/dashboard/projects', async route => {
    if (route.request().method() !== 'POST') return route.continue();
    createRequests.push(route.request().postDataJSON());
    if (createRequests.length === 1) { await createGate; await route.fulfill({ status: 503, json: { code: 'temporarily_unavailable' } }); }
    else await route.continue();
  });
  await page.getByLabel('New project name', { exact: true }).press('Enter');
  assert.equal(await page.locator('.project-entry-form button').evaluateAll(nodes => nodes.every(node => node.disabled)), true);
  assert.equal(await page.getByRole('button', { name: 'Create project', exact: true }).getAttribute('aria-busy'), 'true');
  releaseCreate();
  await page.getByText('Could not complete the action. Check your connection and access, then retry.', { exact: true }).waitFor();
  await page.getByRole('button', { name: 'Create project', exact: true }).click();
  await page.getByRole('button', { name: 'Shared repository', exact: true }).waitFor();
  assert.equal(createRequests.length, 2); assert.deepEqual(createRequests[0], createRequests[1], 'Creation retry preserves the idempotency key');
  await page.unroute('**/v1/dashboard/projects');
  assert.equal(unauthorizedDirectoryRequests, 0, 'Sidebar creation must never authorize a device');
  assert.equal(creationJoins, 0, 'A newly created project must not be joined a second time');
  page.off('request', trackDirectory);
  const projects = await (await context.request.get(fixture.url + '/v1/dashboard/projects')).json();
  assert.equal(projects.projects.length, 1);
  await page.locator('.installation-guide').waitFor();
  assert.equal(await page.locator('.installation-guide pre:visible').textContent(), "coconet connect '" + projects.projects[0].project_id + "' --server '" + fixture.cli_url + "'");
  for (const language of ['zh', 'en']) {
    await page.locator('#login [data-language]').evaluate((select, value) => {
      select.value = value; select.dispatchEvent(new Event('change', { bubbles: true }));
    }, language);
    assert.equal(await page.locator('#modal-title').textContent(), language === 'zh' ? '连接项目目录' : 'Connect a project folder');
    assert.doesNotMatch(await page.locator('.installation-guide').innerText(), /连接到：|Connect to:|Shared repository/);
    for (const width of [1280, 390, 320]) {
      await page.setViewportSize({ width, height: 844 });
      assert.equal(await page.locator('#modal-content').evaluate(node => node.scrollWidth > node.clientWidth + 1), false);
      const connectCommand = "coconet connect '" + projects.projects[0].project_id + "' --server '" + fixture.cli_url + "'";
      assert.equal(await page.locator('.installation-guide .command-block:visible').count(), 1);
      assert.doesNotMatch(await page.locator('#modal-content').innerText(), /npm install/);
      await page.locator('.installation-guide .command-block:visible button').click();
      assert.equal(await page.evaluate(() => window.fixtureClipboard), connectCommand);
      await screenshot(page, 'created-project-guide-' + language + '-' + width);
      await page.locator('[data-disclosure="first-use"] summary').focus();
      await page.keyboard.press('Enter');
      await page.locator('[data-disclosure="first-use"] button').click();
      assert.equal(await page.evaluate(() => window.fixtureClipboard), 'npm install -g coconet');
      assert.equal(await page.locator('#modal-content').evaluate(el => el.scrollWidth > el.clientWidth + 1), false);
      await page.locator('[data-disclosure="first-use"] summary').click();
    }
  }
  // Clipboard failure in the signed-in guide remains recoverable.
  await page.evaluate(() => { navigator.clipboard.writeText = async () => { throw new Error('Clipboard unavailable'); }; });
  await page.locator('.installation-guide .command-block:visible button').click();
  await page.getByText('Could not copy. Please try again.', { exact: true }).waitFor();
  assert.equal(await page.locator('.installation-guide pre:visible').evaluate(node => getComputedStyle(node).userSelect), 'text');
  await page.evaluate(() => { navigator.clipboard.writeText = async text => { window.fixtureClipboard = text; }; });
  await page.locator('.installation-guide .command-block:visible button').click();
  assert.equal(await page.evaluate(() => window.fixtureClipboard), "coconet connect '" + projects.projects[0].project_id + "' --server '" + fixture.cli_url + "'");
  await page.keyboard.press('Escape');
  assert.equal(await page.locator('#project-manage').evaluate(node => node === document.activeElement), true);
  await page.setViewportSize({ width: 1280, height: 844 });
  await page.locator('#project-manage').click();
  assert.equal(await page.getByRole('button', { name: 'Leave project', exact: true }).count(), 0);
  assert.equal(await page.getByRole('button', { name: 'Connect a project folder', exact: true }).count(), 0);
  await page.locator('#modal-close').click();
  await page.locator('#canvas-settings').click();
  assert.equal(await page.locator('#modal-content').getByRole('button', { name: 'View sync status', exact: true }).count(), 0);
  assert.equal(await page.locator('#canvas-help,#project-folders').count(), 0);
  assert.deepEqual(await page.locator('.canvas-dock button').evaluateAll(nodes => nodes.map(node => node.id)), ['refresh', 'project-share', 'canvas-settings', 'zoom-out', 'zoom-in', 'zoom-fit']);
  assert.equal(await page.locator('.project-settings h3,.project-settings > strong').count(), 0);
  assert.equal(await page.getByRole('button', { name: 'Create project', exact: true }).count(), 0);
  assert.equal(await page.getByRole('button', { name: 'Invite a teammate', exact: true }).count(), 0);
  await page.locator('.preferences-language select').focus();
  assert.equal(await page.locator('.preferences-language select').evaluate(node => getComputedStyle(node).outlineStyle), 'none');
  assert.equal(await page.locator('.project-folders').count(), 0);
  await page.locator('#modal-close').click();
  await page.locator('#project-share').click();
  await page.getByText('No folders connected yet.', { exact: true }).waitFor();
  await page.getByRole('button', { name: 'Connect a project folder', exact: true }).click();
  assert.ok((await page.locator('.installation-guide pre').filter({ hasText: 'coconet connect' }).textContent()).includes(projects.projects[0].project_id));
  await screenshot(page, 'connection-instructions');
  await page.locator('#modal-close').click();
  assert.equal(await page.locator('#project-share').evaluate(node => node === document.activeElement), true);
  const me = await (await context.request.get(fixture.url + '/v1/dashboard/session')).json();
  const deviceTokens = [];
  for (const name of ['Mac fixture', 'Linux fixture']) {
    const verifier = randomBytes(32).toString('base64url');
    const challenge = createHash('sha256').update(verifier).digest('base64url');
    const start = await (await context.request.post(fixture.url + '/v1/auth/device', { data: { device_name: name, code_challenge: challenge, callback_url: 'http://127.0.0.1:9/callback' } })).json();
    const pending = await (await context.request.post(fixture.url + '/v1/auth/token', { data: { device_code: start.device_code, code_verifier: verifier } })).json();
    assert.equal(pending.status, 'pending');
    await page.goto(start.verification_uri);
    await page.getByRole('button', { name: 'Confirm connection', exact: true }).waitFor();
    assert.ok((await page.locator('#modal-content').textContent()).includes(name));
    await screenshot(page, 'authorize-device');
    await page.getByRole('button', { name: 'Confirm connection', exact: true }).click();
    await page.locator('#session-modal').waitFor({ state: 'hidden' });
    await page.getByText('Device connected', { exact: true }).waitFor();
    assert.equal(new URL(page.url()).origin, fixture.url);
    const granted = await (await context.request.post(fixture.url + '/v1/auth/token', { data: { device_code: start.device_code, code_verifier: verifier } })).json();
    assert.equal(granted.status, 'ok'); assert.equal(granted.account.account_id, me.account.account_id);
    deviceTokens.push(granted.access_token);
  }
  await page.locator('#account-name').click();
  await page.locator('.device-row').nth(1).waitFor();
  assert.equal(await page.locator('.device-row').count(), 2);
  await screenshot(page, 'account-actions');
  await page.getByLabel('Display name', { exact: true }).fill('Unified developer');
  await page.getByRole('button', { name: 'Save name', exact: true }).click();
  await page.getByText('Name saved.', { exact: true }).waitFor();
  await page.locator('.device-row').filter({ hasText: 'Mac fixture' }).getByRole('button', { name: 'Remove device' }).click();
  await page.locator('.device-row').filter({ hasText: 'Mac fixture' }).waitFor({ state: 'detached' });
  assert.equal((await context.request.get(fixture.url + '/v1/account', { headers: { Authorization: 'Bearer ' + deviceTokens[0] } })).status(), 401);
  assert.equal((await context.request.get(fixture.url + '/v1/account', { headers: { Authorization: 'Bearer ' + deviceTokens[1] } })).status(), 200);
  await page.locator('#modal-close').click();
  await page.reload(); await page.locator('#workspace').waitFor();
  assert.equal(await page.locator('#account-name').textContent(), 'Unified developer');
  // Default CLI setup uses project ID input and the same PKCE exchange.
  for (const selection of ['existing', 'new']) {
    const verifier = randomBytes(32).toString('base64url');
    const challenge = createHash('sha256').update(verifier).digest('base64url');
    const start = await (await context.request.post(fixture.url + '/v1/auth/device', { data: {
      device_name: 'Setup ' + selection, code_challenge: challenge, connect_project: true,
      project_name: 'Suggested folder', expected_account_id: me.account.account_id,
      directory: { cwd: '/work/example/src', path: '/work/example', revision: 1, remote: selection === 'new', can_browse: selection === 'existing' },
    } })).json();
    await page.goto(start.verification_uri);
    await page.locator('.project-entry').waitFor();
    assert.equal(await page.locator('.project-entry-form:visible').count(), 0);
    assert.equal(await page.locator('[data-choice]').count(), 3);
    const selected = selection === 'existing' ? projects.projects[0].project_id : '__new__';
    await chooseEntry(page, selection === 'existing' ? 'existing' : 'create');
    if (selection === 'existing') await page.getByLabel('Choose a project', { exact: true }).selectOption(selected);
    else {
      assert.equal(await page.getByLabel('New project name', { exact: true }).inputValue(), 'Suggested folder');
      await page.getByLabel('New project name', { exact: true }).fill('Browser setup project');
    }
    assert.equal(await page.locator('.connection-path').textContent(), '/work/example');
    assert.equal(await page.getByRole('button', { name: 'Cancel', exact: true }).count(), 0);
    assert.equal(await page.locator('.connection-path').evaluate(node => getComputedStyle(node).textDecorationLine), 'none');
    assert.equal(await page.getByRole('button', { name: 'Decline', exact: true }).count(), 0);
    if (selection === 'existing') {
      for (const cancelled of [true, false]) {
        if (cancelled) await page.locator('.connection-path').click();
        else await page.getByRole('button', { name: 'Choose another folder', exact: true }).click();
        await page.getByText('Choose a folder on the computer running the CLI…', { exact: true }).waitFor();
        assert.equal(await page.locator('.project-entry-form button').evaluateAll(nodes => nodes.every(node => node.disabled)), true);
        const pending = await (await context.request.post(fixture.url + '/v1/auth/token', { data: { device_code: start.device_code, code_verifier: verifier } })).json();
        assert.ok(pending.directory_request_id);
        const update = await context.request.post(fixture.url + '/v1/auth/directory', { data: {
          device_code: start.device_code, code_verifier: verifier, request_id: pending.directory_request_id,
          ...(cancelled ? { error: 'cancelled' } : { cwd: '/work/中文 project/src', path: '/work/中文 project' }),
        } });
        assert.equal(update.status(), 204);
        await page.getByText('Choose a folder on the computer running the CLI…', { exact: true }).waitFor({ state: 'detached' });
        assert.equal(await page.locator('.connection-path').textContent(), cancelled ? '/work/example' : '/work/中文 project');
        assert.equal(await page.getByLabel('Choose a project', { exact: true }).inputValue(), selected);
      }
      if (process.env.COCONET_ACCOUNT_SCREENSHOT) await page.screenshot({ path: process.env.COCONET_ACCOUNT_SCREENSHOT });
      await page.setViewportSize({ width: 390, height: 844 });
      assert.equal(await page.locator('.connection-card').evaluate(node => node.scrollWidth <= node.clientWidth), true);
      await screenshot(page, 'connection-approval-mobile');
      await page.setViewportSize({ width: 1280, height: 800 });
      await screenshot(page, 'connection-approval');
      for (const language of ['en', 'zh']) {
        const sharingContext = await browser.newContext({ ignoreHTTPSErrors: true, storageState: await context.storageState() });
        await sharingContext.addInitScript(value => localStorage.setItem('coconet.dashboard.language', value), language);
        const sharingPage = await sharingContext.newPage();
        sharingPage.on('pageerror', error => errors.push(error.message));
        await sharingPage.goto(start.verification_uri);
        await chooseEntry(sharingPage, 'create');
        await sharingPage.getByLabel(language === 'zh' ? '新项目名称' : 'New project name', { exact: true }).fill('较长的协作项目名称 / Long collaboration project');
        assert.equal(await sharingPage.locator('[data-disclosure="sharing-scope"],.connection-modes').count(), 0);
        assert.match(await sharingPage.locator('.connection-folder-line').innerText(), language === 'zh' ? /当前正在连接文件夹：/ : /Connecting folder:/);
        assert.doesNotMatch(await sharingPage.locator('#modal-content').innerText(), /已有历史|工具调用|Existing history|tool calls/);
        for (const width of [1440, 390, 320]) {
          await sharingPage.setViewportSize({ width, height: 900 });
          assert.equal(await sharingPage.locator('#modal-content').evaluate(el => el.scrollWidth > el.clientWidth + 1), false);
          await checkProjectChoices(sharingPage, width, true);
          assert.equal(await sharingPage.locator('.project-entry input:visible').count(), 1);
          await screenshot(sharingPage, `connection-columns-${language}-${width}`);
        }
        // A long directory remains fully readable, including on narrow screens.
        const longPath = '/work/团队协作/' + 'long-directory-name-'.repeat(8) + '/project';
        await sharingPage.route('**/v1/dashboard/authorize', async route => {
          const response = await route.fetch(), data = await response.json();
          data.directory.path = longPath;
          await route.fulfill({ response, json: data });
        });
        await sharingPage.reload();
        await sharingPage.locator('.project-entry').waitFor();
        assert.equal(await sharingPage.locator('.connection-path').textContent(), longPath);
        assert.equal(await sharingPage.locator('#modal-content').evaluate(el => el.scrollWidth > el.clientWidth + 1), false);
        await screenshot(sharingPage, `connection-columns-long-path-${language}-320`);
        await sharingPage.goto(fixture.url + '/');
        await sharingPage.locator('#project-manage').click();
        for (const width of [1440, 768, 390, 320]) {
          await sharingPage.setViewportSize({ width, height: 900 });
          await checkProjectChoices(sharingPage, width, false);
          await screenshot(sharingPage, `project-choices-${language}-${width}`);
        }
        await sharingPage.keyboard.press('Escape');
        assert.equal(await sharingPage.locator('#project-manage').evaluate(node => node === document.activeElement), true);
        await sharingContext.close();
      }
    } else {
      assert.equal(await page.getByRole('button', { name: 'Choose another folder', exact: true }).count(), 0);
      // A failed approval after creation must retry the same project.
      await page.route('**/v1/dashboard/authorize', route => route.fulfill({ status: 503, json: { code: 'temporarily_unavailable' } }));
      await page.getByLabel('New project name', { exact: true }).press('Enter');
      await page.getByText('Could not complete the action. Check your connection and access, then retry.', { exact: true }).waitFor();
      assert.equal((await (await context.request.get(fixture.url + '/v1/dashboard/projects')).json()).projects.filter(item => item.name === 'Browser setup project').length, 1);
      await page.unroute('**/v1/dashboard/authorize');
    }
    if (selection === 'existing') await page.getByRole('button', {name:'Connect project',exact:true}).click();
    else await page.getByLabel('New project name', {exact:true}).press('Enter');
    await page.locator('#session-modal').waitFor({ state: 'hidden' });
    await page.getByText('Device connected', { exact: true }).waitFor();
    assert.equal(new URL(page.url()).origin, fixture.url);
    const grant = await (await context.request.post(fixture.url + '/v1/auth/token', { data: { device_code: start.device_code, code_verifier: verifier } })).json();
    assert.equal(grant.status, 'ok'); assert.equal(grant.account.account_id, me.account.account_id);
    assert.ok(grant.project_id);
    assert.equal(grant.directory.path, selection === 'existing' ? '/work/中文 project' : '/work/example');
    assert.equal(grant.directory.revision, selection === 'existing' ? 3 : 1);
    if (selection === 'existing') assert.equal(grant.project_id, selected);
    else assert.equal((await (await context.request.get(fixture.url + '/v1/dashboard/projects')).json()).projects.filter(item => item.name === 'Browser setup project').length, 1);
    assert.equal(new URL(page.url()).pathname, '/' + grant.project_id);
  }
  // Cancel declines this authorization and does not create a project.
  const cancelVerifier = randomBytes(32).toString('base64url');
  const cancelStart = await (await context.request.post(fixture.url + '/v1/auth/device', { data: {
    device_name: 'Cancel fixture', code_challenge: createHash('sha256').update(cancelVerifier).digest('base64url'), connect_project: true,
    project_name: 'Cancelled project', directory: { cwd: '/work/cancel', path: '/work/cancel', revision: 1, remote: true, can_browse: false },
  } })).json();
  await page.goto(cancelStart.verification_uri);
  await page.locator('#modal-close').click();
  await page.locator('#session-modal').waitFor({ state: 'hidden' });
  await page.getByText('Connection cancelled.', { exact: true }).waitFor();
  const cancelled = await context.request.post(fixture.url + '/v1/auth/token', { data: { device_code: cancelStart.device_code, code_verifier: cancelVerifier } });
  assert.equal(cancelled.status(), 403);
  assert.equal((await cancelled.json()).code, 'authorization_denied');
  assert.equal((await (await context.request.get(fixture.url + '/v1/dashboard/projects')).json()).projects.filter(item => item.name === 'Cancelled project').length, 0);
  // Exercise the actual CLI through desktop launch, SSH and opener failure.
  // Agent roots are empty and isolated; this does not invoke an Agent or model.
  const binary = join(temporary, 'coconet');
  const build = spawn('go', ['test', '-c', '-o', binary, './apps/runtime'], { stdio: 'pipe' });
  assert.equal(await new Promise(resolve => build.on('exit', resolve)), 0);
  const opener = join(temporary, 'browser-bin'); await mkdir(opener);
  for (const name of ['open', 'xdg-open']) await writeFile(join(opener, name), '#!/bin/sh\nprintf \"%s\" \"$1\" > \"$COCONET_BROWSER_CAPTURE\"\nexit \"$COCONET_BROWSER_EXIT\"\n', { mode: 0o755 });
  for (const mode of ['remote', 'desktop', 'fallback']) {
    const folder = join(temporary, mode + ' 中文 workspace'); await mkdir(folder);
    const runtimeHome = join(temporary, 'runtime-' + mode, 'coconet'), capture = join(temporary, mode + '-browser-url');
    cli = spawn(binary, ['-test.run=^TestBrowserCLIProcess$', '--', 'connect', ...(mode === 'remote' ? ['--project', projects.projects[0].project_id] : [projects.projects[0].project_id]), '--path', folder, '--server', fixture.cli_url], {
      cwd: temporary, env: { ...process.env, XDG_CONFIG_HOME: join(runtimeHome, '..'), COCONET_CWD: temporary,
        COCONET_BROWSER_FIXTURE_CA: fixture.certificate,
        SSH_CONNECTION: mode === 'remote' ? 'isolated remote fixture' : '', SSH_TTY: '',
        PATH: opener + ':' + process.env.PATH, COCONET_BROWSER_CAPTURE: capture, COCONET_BROWSER_EXIT: mode === 'fallback' ? '1' : '0',
        CODEX_HOME: join(temporary, 'codex-fixture'), CLAUDE_CONFIG_DIR: join(temporary, 'tclaude'),
        COCONET_CODEX_COMMAND: 'fixture-no-agent', COCONET_CLAUDE_COMMAND: 'fixture-no-agent',
        COCONET_DISABLE_BACKGROUND_SYNC: '1' }, stdio: ['ignore', 'pipe', 'pipe'],
    });
    let cliOutput = '', cliError = '', cliExit;
    cli.stdout.on('data', chunk => { cliOutput += chunk; }); cli.stderr.on('data', chunk => { cliError += chunk; });
    const cliDone = new Promise(resolve => cli.on('exit', code => { cliExit = code; resolve(code); }));
    let link;
    for (let i = 0; i < 200; i++) {
      link = cliOutput.match(/https?:\/\/[^\s]+\/#authorize=[A-Za-z0-9_-]{43}/);
      if (link || cliExit !== undefined) break;
      await delay(100);
    }
    assert.ok(link, 'CLI did not offer browser authorization: ' + cliError);
    if (mode !== 'remote') assert.equal(await readFile(capture, 'utf8'), link[0]);
    else await assert.rejects(readFile(capture), { code: 'ENOENT' });
    if (mode === 'fallback') assert.match(cliOutput, /Could not open your browser/);
    assert.doesNotMatch(cliOutput, /Choose a project in your browser|Server:/);
    const code = new URLSearchParams(new URL(link[0]).hash.slice(1)).get('authorize');
    assert.doesNotMatch(cliOutput, /  Code:/);
    assert.match(cliOutput, /Connection Information\n  Device: [^\n]+\n  Folder: [^\n]+\nBrowser Confirmation\n  /);
    assert.doesNotMatch(cliOutput, /Waiting for browser authorization|If the browser did not open/);
    assert.equal(code.length, 43);
    // A fresh SSH device and signed-out browser must finish login before approval.
    if (mode === 'remote') {
      await page.locator('#account-name').click();
      await page.locator('#logout').click();
      await page.locator('#login').waitFor();
    }
    assert.equal(new URL(link[0]).origin, fixture.url, 'CLI must receive the configured browser origin');
    await page.goto(link[0]);
    if (mode === 'remote') {
      await page.getByRole('button', { name: 'Continue with GitHub' }).waitFor();
      assert.equal(await page.getByRole('button', { name: 'Join project', exact: true }).count(), 0);
      assert.equal(cliExit, undefined, 'Unsigned device must wait for browser approval');
      await assert.rejects(readFile(join(runtimeHome, 'bindings.json')), { code: 'ENOENT' });
      await page.getByRole('button', { name: 'Continue with GitHub' }).click();
    }
    await page.getByRole('button', { name: 'Connect project', exact: true }).waitFor();
    assert.equal(await page.getByLabel('Choose a project', { exact: true }).inputValue(), projects.projects[0].project_id);
    assert.equal(await page.locator('.authorization-code').count(), 0);
    assert.equal(await page.locator('.connection-path').textContent(), await realpath(folder));
    assert.equal(await page.getByRole('button', { name: 'Choose another folder', exact: true }).count(), mode === 'remote' ? 0 : 1);
    await page.getByRole('button', { name: 'Connect project', exact: true }).click();
    await page.locator('#session-modal').waitFor({ state: 'hidden' });
    assert.equal(new URL(page.url()).pathname, '/' + projects.projects[0].project_id);
    assert.equal(await Promise.race([cliDone, delay(10000).then(() => 'timeout')]), 0, cliError);
    assert.match(cliOutput, /✓ Connected to Shared repository\n$/);
    assert.doesNotMatch(cliOutput, /Dashboard|Sync |Account |New conversation updates/);
    const binding = JSON.parse(await readFile(join(runtimeHome, 'bindings.json'), 'utf8'));
    assert.equal(binding.bindings.length, 1);
    assert.equal(binding.bindings[0].collaboration_root, await realpath(folder));
    assert.equal(binding.bindings[0].project_id, projects.projects[0].project_id);
  }
  // Real CLI bindings are reported only after the local file is committed.
  await page.locator('#project-share').click();
  await page.locator('.folder-row').nth(2).waitFor();
  assert.equal(await page.locator('.folder-row').count(), 3);
  assert.equal(await page.locator('.folder-row[open]').count(), 0);
  const folder = page.locator('.folder-row').first();
  assert.equal(await folder.locator('summary').getAttribute('title'), await folder.locator('code').textContent());
  assert.equal(await folder.locator('code').isVisible(), false);
  await folder.locator('summary').focus(); await page.keyboard.press('Enter');
  assert.equal(await folder.locator('code').isVisible(), true);
  await page.keyboard.press('Enter'); assert.equal(await folder.locator('code').isVisible(), false);
  assert.equal(await page.getByRole('button', { name: 'Copy Project ID', exact: true }).count(), 0);
  assert.equal(await page.locator('.share-result .copy-field').count(), 1);
  for (const name of ['Copy project link']) {
    const control = page.getByRole('button', { name, exact: true });
    assert.equal(await control.evaluate(node => getComputedStyle(node).backgroundColor), 'rgb(255, 255, 255)');
    assert.equal(await control.evaluate(node => node.getBoundingClientRect().height), 40);
  }

  assert.equal(await page.locator('#modal-content .account-content > strong').count(), 0);
  const registered = await (await context.request.get(fixture.url + '/v1/dashboard/projects/' + projects.projects[0].project_id + '/workspaces')).json();
  assert.equal(registered.folders.length, 3);
  assert.ok(registered.folders.every(folder => folder.display_name === 'Unified developer' && folder.path.endsWith(' 中文 workspace')));
  await screenshot(page, 'connected-folders');
  await page.locator('#modal-close').click();
  // Folder failure stays in its section; sharing remains usable and retry recovers.
  const workspaces = '**/v1/dashboard/projects/' + projects.projects[0].project_id + '/workspaces?*';
  // Optional directory guidance: paginate, preserve focus, and ignore late results.
  const intro = page.locator('.installation-guide > section > p').first();
  const genericIntro = 'Run the following command in your project folder:';
  let example = 'mcp-zero', exampleFailure = false, exampleConflict = false, releaseExample, exampleStarted;
  let exampleGate = Promise.resolve(), examplePending, exampleFinished;
  const exampleRoute = async route => {
    const url = new URL(route.request().url());
    if (url.searchParams.get('limit') !== '200') return route.continue();
    exampleStarted?.(); await exampleGate;
    if (exampleFailure) return route.fulfill({ status: 503, json: { code: 'temporarily_unavailable' } });
    const next = url.searchParams.has('cursor');
    await route.fulfill({ json: { folders: [{ path: (next ? '/srv/team/' : '/home/member/') + (next && exampleConflict ? 'another-repo' : example) }], next_cursor: next ? '' : 'next' } });
    exampleFinished?.();
  };
  await page.route(workspaces, exampleRoute);
  const openGuide = async () => {
    await page.locator('#project-share').click();
    await page.getByRole('button', { name: 'Connect a project folder', exact: true }).click();
    await page.locator('.installation-guide').waitFor();
  };
  exampleGate = new Promise(resolve => { releaseExample = resolve; });
  examplePending = new Promise(resolve => { exampleStarted = resolve; });
  await openGuide(); await examplePending;
  assert.equal(await intro.textContent(), genericIntro);
  await page.locator('[data-disclosure="first-use"] summary').focus();
  await page.keyboard.press('Enter');
  releaseExample();
  await page.getByText('Run the following command in your project folder (for example, mcp-zero/):', { exact: true }).waitFor();
  assert.equal(await page.locator('[data-disclosure="first-use"]').getAttribute('open'), '');
  assert.equal(await page.locator('[data-disclosure="first-use"] summary').evaluate(node => node === document.activeElement), true);
  await page.keyboard.press('Escape');
  exampleStarted = null;
  // Long names are plain text and wrap in both languages on narrow screens.
  example = 'collaboration-project-'.repeat(9) + '<svg onload=alert(1)>';
  await openGuide(); await intro.filter({ hasText: example + '/' }).waitFor();
  assert.equal(await intro.locator('svg').count(), 0);
  for (const language of ['zh', 'en']) {
    await page.locator('#login [data-language]').evaluate((select, value) => { select.value = value; select.dispatchEvent(new Event('change', { bubbles: true })); }, language);
    assert.ok((await intro.textContent()).includes(example + '/'));
    for (const width of [1280, 390, 320]) {
      await page.setViewportSize({ width, height: 844 });
      assert.equal(await page.locator('#modal-content').evaluate(node => node.scrollWidth > node.clientWidth + 1), false);
      await screenshot(page, 'directory-example-' + language + '-' + width);
    }
  }
  await page.keyboard.press('Escape');
  for (const failure of [false, true]) {
    exampleConflict = !failure; exampleFailure = failure;
    const response = page.waitForResponse(r => r.url().includes('/workspaces?limit=200') && (failure || r.url().includes('cursor=')));
    await openGuide(); await response;
    assert.equal(await intro.textContent(), genericIntro);
    await page.locator('.installation-guide .command-block:visible button').click();
    assert.ok((await page.evaluate(() => window.fixtureClipboard)).includes(projects.projects[0].project_id));
    await page.keyboard.press('Escape');
  }
  example = 'recovered'; exampleConflict = false; exampleFailure = false;
  await openGuide(); await intro.filter({ hasText: 'recovered/' }).waitFor();
  await page.keyboard.press('Escape');
  exampleGate = new Promise(resolve => { releaseExample = resolve; });
  examplePending = new Promise(resolve => { exampleStarted = resolve; });
  await openGuide(); await examplePending;
  await page.keyboard.press('Escape'); await page.locator('#canvas-settings').click();
  const finishedExample = new Promise(resolve => { exampleFinished = resolve; });
  releaseExample(); await finishedExample; await page.unroute(workspaces, exampleRoute);
  assert.equal(await page.locator('#modal-title').textContent(), 'Settings');
  assert.equal(await page.locator('.installation-guide').count(), 0);
  await page.keyboard.press('Escape');
  await page.route(workspaces, route => route.fulfill({ status: 503, json: { code: 'temporarily_unavailable' } }));
  await page.locator('#project-share').click();
  await page.getByRole('button', { name: 'Copy project link', exact: true }).waitFor();
  await page.locator('.shared-folders').getByRole('button', { name: 'Retry', exact: true }).waitFor();
  await page.unroute(workspaces);
  await page.locator('.shared-folders').getByRole('button', { name: 'Retry', exact: true }).click();
  await page.locator('.folder-row').nth(2).waitFor();
  await page.keyboard.press('Escape');
  await page.locator('#project-share').click();
  await page.getByRole('heading', { name: 'Share project', exact: true }).waitFor();
  await page.getByRole('button', { name: 'Copy project link', exact: true }).waitFor();
  const invitation = await page.locator('.share-result .copy-field pre').textContent();
  assert.equal(await page.locator('.share-result .copy-field pre').isVisible(), true);
  await page.evaluate(() => { navigator.clipboard.writeText = async () => { throw new Error('fixture clipboard failure'); }; });
  await page.getByRole('button', { name: 'Copy project link', exact: true }).click();
  await page.getByText('Could not copy. Please try again.', { exact: true }).waitFor();
  await page.evaluate(() => { navigator.clipboard.writeText = async text => { window.fixtureClipboard = text; }; });
  assert.equal(new URL(invitation).origin, fixture.url);
  assert.equal(new URL(invitation).pathname, '/' + projects.projects[0].project_id);
  await page.getByRole('button', { name: 'Copy project link', exact: true }).click();
  assert.equal(await page.evaluate(() => window.fixtureClipboard), invitation);
  await screenshot(page, 'share-collapsed');

  assert.equal(await page.locator('.share-result .copy-field pre').isVisible(), true);
  await screenshot(page, 'share-expanded');
  for (const width of [390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    assert.equal(await page.locator('#modal-content').evaluate(node => node.scrollWidth <= node.clientWidth), true);
    await screenshot(page, 'share-expanded-' + width);
  }

  assert.equal(await page.locator('.share-result .copy-field pre').isVisible(), true);
  await screenshot(page, 'share-collapsed-320');
  await page.locator('#modal-close').click();
  assert.equal(await page.locator('#project-share').evaluate(node => node === document.activeElement), true);
  for (const language of ['zh', 'en']) {
    await page.locator('#canvas-settings').click();
    await page.locator('.preferences-language select').selectOption(language);
    await page.keyboard.press('Escape');
    await page.locator('#project-share').click();
    await page.locator('.folder-row').nth(2).waitFor();
    await page.getByRole('button', { name: language === 'zh' ? '复制项目链接' : 'Copy project link', exact: true }).waitFor();
    assert.equal(await page.locator('.shared-folders h3').textContent(), language === 'zh' ? '已连接的文件夹' : 'Connected folders');
    assert.equal(await page.locator('.share-content').evaluate(el => el.querySelector('.share-section').getBoundingClientRect().bottom < el.querySelector('.shared-folders').getBoundingClientRect().top), true);
    for (const width of [1280, 390, 320]) {
      await page.setViewportSize({ width, height: 844 });
      assert.equal(await page.locator('#modal-content').evaluate(el => el.scrollWidth > el.clientWidth + 1), false);
      await screenshot(page, 'share-folders-' + width + '-' + language);
    }
    await page.locator('#modal-close').focus();
    await page.keyboard.press('Shift+Tab');
    assert.equal(await page.locator('#session-modal').evaluate(el => el.contains(document.activeElement)), true);
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('#project-share').evaluate(el => el === document.activeElement), true);
  }
  const dock = await page.locator('.canvas-dock').boundingBox();
  assert.ok(dock.x >= 0 && dock.x + dock.width <= 320, 'Toolbar should fit at 320 px');
  await page.setViewportSize({ width: 1280, height: 720 });
  const second = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'zh-CN' });
  await second.addCookies([{ name: 'fixture_identity', value: 'teammate', url: fixture.url, secure: true, httpOnly: true }]);
  const otherPage = await second.newPage();
  await otherPage.goto(invitation);
  await otherPage.getByRole('button', { name: '使用 GitHub 登录' }).click();
  await otherPage.locator('#workspace').waitFor();
  assert.equal(await otherPage.locator('#account-name').textContent(), 'Browser teammate');
  const teammate = await (await second.request.get(fixture.url + '/v1/dashboard/session')).json();
  assert.notEqual(teammate.account.account_id, me.account.account_id);
  assert.equal((await (await second.request.get(fixture.url + '/v1/dashboard/projects')).json()).projects.length, 0);
  await checkProjectNotice(otherPage, 'zh', false, projects.projects[0].name);
  let invitationJoins = 0, invitationDirectoryRequests = 0;
  otherPage.on('request', request => {
    if (request.url().endsWith('/v1/dashboard/connections')) invitationJoins++;
    if (request.url().endsWith('/v1/dashboard/authorize')) invitationDirectoryRequests++;
  });
  await otherPage.getByRole('button', { name: '加入项目', exact: true }).click();
  await checkProjectNotice(otherPage, 'zh', true, projects.projects[0].name);
  assert.equal((await (await second.request.get(fixture.url + '/v1/dashboard/projects')).json()).projects.length, 1);
  await otherPage.getByRole('button', { name: '如何连接项目目录', exact: true }).click();
  await otherPage.locator('.command-block').filter({ hasText: 'coconet connect' }).getByRole('button', { name: '复制命令', exact: true }).waitFor();
  const joinCommand = await otherPage.locator('.installation-guide pre').filter({ hasText: 'coconet connect' }).textContent();
  assert.match(joinCommand, /^coconet connect 'coco_/);
  assert.ok(joinCommand.includes(" --server '" + fixture.cli_url + "'"), 'Copied commands must target the API origin');
  assert.equal(invitationJoins, 1, 'Opening instructions must not join the project again');
  assert.equal(invitationDirectoryRequests, 0, 'Viewing instructions must not authorize a device');
  await otherPage.locator('#modal-close').click();
  assert.equal(await otherPage.locator('.project-item').count(), 1);

  // Leaving is scoped to the selected project's settings and requires an
  // explicit confirmation. A failed request remains retryable in the dialog.
  await otherPage.locator('#canvas-settings').click();
  await otherPage.getByRole('button', { name: '退出此项目', exact: true }).click();
  assert.equal((await (await second.request.get(fixture.url + '/v1/dashboard/projects')).json()).projects.length, 1);
  await screenshot(otherPage, 'leave-confirmation');
  await otherPage.getByRole('button', { name: '取消', exact: true }).click();
  assert.equal(await otherPage.getByRole('button', { name: '确认退出项目', exact: true }).count(), 0);
  assert.equal(await otherPage.getByRole('button', { name: '退出此项目', exact: true }).evaluate(node => node === document.activeElement), true);
  await otherPage.getByRole('button', { name: '退出此项目', exact: true }).click();
  const membership = '**/v1/dashboard/projects/' + projects.projects[0].project_id + '/membership';
  await otherPage.route(membership, route => route.fulfill({ status: 503, json: { code: 'temporarily_unavailable' } }));
  await otherPage.getByRole('button', { name: '确认退出项目', exact: true }).click();
  await otherPage.locator('.project-settings [role="alert"]').filter({ hasText: /.+/ }).waitFor();
  await otherPage.unroute(membership);
  await otherPage.getByRole('button', { name: '确认退出项目', exact: true }).click();
  await otherPage.locator('#project-list.projects-empty').waitFor();
  await otherPage.locator('#project-manage').click();
  await chooseEntry(otherPage, 'join');
  await otherPage.getByRole('textbox', { name: '通过分享链接加入', exact: true }).fill(invitation.replace(fixture.url, fixture.cli_url));
  let joinDirectoryRequests = 0;
  otherPage.on('request', request => { if (request.url().endsWith('/v1/dashboard/authorize')) joinDirectoryRequests++; });
  let releaseMembership;
  const membershipGate = new Promise(resolve => { releaseMembership = resolve; });
  await otherPage.route('**/v1/dashboard/connections', async route => { await membershipGate; await route.fulfill({ status: 503, json: { code: 'temporarily_unavailable' } }); });
  await otherPage.getByRole('textbox', { name: '通过分享链接加入', exact: true }).press('Enter');
  assert.equal(await otherPage.getByRole('button', { name: '加入项目', exact: true }).isDisabled(), true);
  // Closing an in-flight operation must not affect a newly opened dialog.
  await otherPage.locator('#modal-close').focus();
  await otherPage.keyboard.press('Escape');
  await otherPage.locator('#session-modal').waitFor({ state: 'hidden' });
  await otherPage.locator('#project-manage').click();
  releaseMembership();
  await otherPage.unrouteAll({ behavior: 'wait' });
  assert.equal(await otherPage.locator('.project-entry').isVisible(), true);
  assert.equal(await otherPage.locator('.account-error').textContent(), '');
  await chooseEntry(otherPage, 'join');
  await otherPage.getByRole('textbox', { name: '通过分享链接加入', exact: true }).fill(invitation.replace(fixture.url, fixture.cli_url));
  await otherPage.getByRole('textbox', { name: '通过分享链接加入', exact: true }).press('Enter');
  await otherPage.getByRole('heading', { name: '已加入「' + projects.projects[0].name + '」', exact: true }).waitFor();
  assert.equal(await otherPage.locator('.installation-guide').count(), 0);
  await otherPage.keyboard.press('Escape');
  await otherPage.locator('#session-modal').waitFor({ state: 'hidden' });
  await otherPage.locator('.project-item').waitFor();
  assert.equal(await otherPage.locator('.project-item').count(), 1);
  assert.equal(joinDirectoryRequests, 0, 'Joining from the sidebar must never authorize a device');
  await page.locator('#account-name').click();
  await page.route('**/v1/dashboard/session', route => route.request().method() === 'DELETE' ? route.fulfill({ status: 503, json: { code: 'temporarily_unavailable' } }) : route.continue());
  await page.locator('#logout').click();
  await page.getByText('Could not sign out. Please try again.', { exact: true }).waitFor();
  assert.equal(await page.locator('#workspace').isVisible(), true);
  await page.unroute('**/v1/dashboard/session');
  await page.locator('#logout').click(); await page.locator('#login').waitFor();
  assert.equal((await context.request.get(fixture.url + '/v1/account', { headers: { Authorization: 'Bearer ' + deviceTokens[1] } })).status(), 200);
  assert.equal((await second.request.get(fixture.url + '/v1/dashboard/session')).status(), 200);
  await page.route('**/v1/auth/start', route => route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'fixture_unavailable' } }) }));
  await page.getByRole('button', { name: 'Continue with GitHub' }).click();
  await page.locator('#login-error').filter({ hasText: /.+/ }).waitFor({ state: 'visible' });
  assert.equal(await page.getByRole('button', { name: 'Continue with GitHub' }).isEnabled(), true);
  await page.unroute('**/v1/auth/start');
  assert.deepEqual(errors, []);
  console.log('PASS isolated OAuth members, pending device approval and project link continuation, browser OAuth callback, project creation, two device approvals, PKCE exchange, profile persistence, device revocation, browser project setup (select/create), native chooser protocol/cancellation, desktop/SSH/failing-opener actual CLI --path approval and binding, project redirect without loopback navigation, mobile layout, toolbar sharing and a distinct invited account across OAuth with project connection instructions, bilingual login, independent browser logout and visible login failure');
} finally {
  if (cli && cli.exitCode === null) cli.kill('SIGTERM');
  await browser?.close(); await writeFile(file + '.stop', 'stop');
  const result = await Promise.race([done, delay(5000).then(() => { helper.kill('SIGTERM'); return 1; })]);
  if (result !== 0) { process.stderr.write(output); process.exitCode = 1; }
  await rm(temporary, { recursive: true, force: true });
}
