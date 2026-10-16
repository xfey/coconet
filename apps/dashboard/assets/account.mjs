import { mountProjectConnection, mountProjectChoices, connectionErrorKey } from "./connection.mjs";
import { commandBlock, dialogActions as actions } from './dialog.mjs';
import { createButton, setButtonLabel } from "./button.mjs";
import { parseWorkspaceLocation, workspaceLocation } from './catalog.mjs';
// Account and project setup use the same server authority as the canvas.
// Provider credentials and device tokens never enter browser application state.
export function createAccountUI({ state, api, t, element, statusMessage, openModal, closeModal, setModalDismiss, openWorkspace, clearWorkspace, toast, openInstallation }) {
  const $ = id => document.getElementById(id);
  let provider = null;
  let activeCode = '';
  let authorizationContent = null;
  const request = (path, method, data) => api(path, { method, ...(data === undefined ? {} : { body: JSON.stringify(data) }) });
  const failure = node => error => { if (node.isConnected) node.textContent = t(connectionErrorKey(error)); };
  function action(label, run, className = '') {
    const glyph = { save_profile: 'check', revoke_device: 'close', retry: 'refresh', load_more: 'chevronDown', logout: 'logout', leave_project: 'logout', confirm_leave_project: 'logout', authorize_approve: 'check', authorize_decline: 'close', cancel: 'close', join_project: 'login', connect_my_folder: 'folder' }[label];
    const button = createButton(t(label), { glyph, className });
    button.type = 'button';
    button.addEventListener('click', async () => {
      button.disabled = true;
      try { await run(); } finally { button.disabled = false; }
    });
    return button;
  }
  function field(label, value = '', maxLength = 120) {
    const wrapper = element('label', t(label), 'account-field');
    const input = element('input'); input.value = value; input.maxLength = maxLength;
    wrapper.append(input); return { wrapper, input };
  }
  function panel(title, trigger, kind = 'account') {
    openModal(kind, t(title), trigger);
    const content = element('div', undefined, 'account-content');
    const error = element('p', '', 'account-error'); error.setAttribute('role', 'alert');
    $('modal-content').replaceChildren(content, error);
    return { content, error };
  }

  async function loadLogin() {
    provider = await api('/v1/auth/config');
    const keyMode = provider.provider === 'key';
    $('login-form').hidden = !keyMode;
    $('key-help').hidden = !keyMode;
    $('provider-login').hidden = keyMode;
    setButtonLabel($('provider-login'), t('login_with', { provider: provider.label }));
    if (new URLSearchParams(location.hash.slice(1)).has('auth_error')) statusMessage('login-error', 'login_failed');
  }
  async function afterLogin() {
    const code = new URLSearchParams(location.hash.slice(1)).get('authorize');
    if (!state.session) return;
    if (!code) {
      const projectID = parseWorkspaceLocation(location).project;
      if (projectID && !state.projects.some(project => project.project_id === projectID)) projects(projectID);
      return;
    }
    if (activeCode === code && authorizationContent?.isConnected) return;
    activeCode = code;
    const { content, error } = panel('authorize_device', 'account-name');
    authorizationContent = content;
    try {
      const preview = await request('/authorize', 'POST', { authorization_id: code });
      if (!content.isConnected) return;
      if (!preview.connect_project) content.append(element('p', t('authorize_as', { name: state.session.account.display_name }), 'dialog-description'));
      if (!preview.connect_project) content.append(element('strong', preview.device_name), element('p', t('authorize_explanation'), 'dialog-description'));
      async function decide(approve, projectID = '', directoryRevision = 0) {
        await request('/authorize', 'POST', { authorization_id: code, approve, ...(projectID ? { project_id: projectID } : {}), ...(directoryRevision ? { directory_revision: directoryRevision } : {}) });
        if (!content.isConnected) return;
        const destination = projectID || state.project;
        history.replaceState(null, '', workspaceLocation(destination));
        activeCode = '';
        closeModal();
        if (approve && projectID) await openWorkspace(state.session);
        toast(approve ? 'authorization_done' : preview.connect_project ? 'connection_cancelled' : 'authorization_declined');
      }
      const controls = actions();
      if (preview.connect_project) {
        $('modal-title').textContent = t('join_project');
        content.closest('dialog').dataset.kind = 'connection';
        setModalDismiss(mountProjectConnection({ content, error, preview, authorizationID: code, projects: state.projects, request, decide, close: closeModal, t, element }));
      } else {
        controls.append(action('authorize_decline', async () => { try { await decide(false); } catch (caught) { failure(error)(caught); } }), action('authorize_approve', async () => { try { await decide(true); } catch (caught) { failure(error)(caught); } }, 'primary'));
      }
      if (controls.childElementCount) content.append(controls);
    } catch (caught) { failure(error)(caught); activeCode = ''; }
  }
  async function settings() {
    const { content, error } = panel('account_settings', 'account-name', 'profile');
    const profile = field('display_name', state.session.account.display_name);
    const save = action('save_profile', async () => {
      try {
        const account = await request('/profile', 'PUT', { display_name: profile.input.value });
        if (!content.isConnected) return;
        state.session.account = account;
        $('account-label').textContent = state.session.account.display_name;
        error.textContent = ''; toast('profile_saved');
      } catch (caught) { failure(error)(caught); }
    }, 'primary');
    const devices = element('div', undefined, 'device-list');
    const logout = action('logout', async () => {
      try { await request('/session', 'DELETE'); clearWorkspace(); }
      catch { error.textContent = t('logout_error'); }
    }, 'danger');
    logout.id = 'logout';
    const footer = actions(logout); footer.classList.add('account-footer');
    const editor = element('div', undefined, 'profile-editor'); editor.append(profile.wrapper, actions(save));
    const deviceSection = element('section', undefined, 'dialog-section'); deviceSection.append(element('h3', t('authorized_devices')), devices);
    content.append(editor, deviceSection, footer);
    try {
      const response = await api('/devices');
      if (!content.isConnected) return;
      if (!response.devices.length) devices.append(element('p', t('devices_empty')));
      for (const device of response.devices) {
        const row = element('div', undefined, 'device-row');
        const label = element('div'); label.append(element('strong', device.name), element('small', t('device_expires', { time: new Date(device.expires_at).toLocaleString() })));
        row.append(label, action('revoke_device', async () => {
          try { await request('/devices/' + encodeURIComponent(device.id), 'DELETE'); row.remove(); }
          catch (caught) { failure(error)(caught); }
        }));
        devices.append(row);
      }
    } catch (caught) { failure(error)(caught); }
  }
  function connectionHelp(project, joined = false, trigger = 'project-manage') {
    openInstallation({ project, joined, trigger });
  }
  function shareProject(project, trigger = 'project-manage') {
    if (!project) return;
    const { content, error } = panel('share_project', trigger, 'share');
    content.classList.add('share-content');
    const sharing = element('section', undefined, 'dialog-section share-section');
    const summary = element('div', undefined, 'share-summary');
    summary.append(element('strong', project.name), element('p', t(project.status === 'active' ? 'invite_instruction' : 'archived'), 'muted'));
    const resultArea = element('div', undefined, 'share-result');
    sharing.append(summary, resultArea, error);
    const folders = element('section', undefined, 'dialog-section shared-folders');
    folders.append(element('h3', t('connected_folders')));
    const folderList = element('div', undefined, 'project-folders');
    folders.append(folderList);
    if (project.status === 'active') folders.append(actions(action('connect_my_folder', () => connectionHelp(project, false, trigger), 'primary')));
    content.append(sharing, folders);
    renderProjectFolders(folderList, project.project_id);
    if (project.status !== 'active') return;
    const url = location.origin + workspaceLocation(project.project_id);
    const copyOptions = {
      element, t,
      copyText: async text => { try { await navigator.clipboard.writeText(text); if (content.isConnected) toast('copy_success'); } catch { if (content.isConnected) toast('copy_failed'); } },
    };
    for (const [label, value, copy] of [['project_id', project.project_id, 'copy_project_id'], ['project_link', url, 'copy_project_link']]) {
      const group = element('div', undefined, 'copy-field');
      group.append(element('h3', t(label)), commandBlock(value, { ...copyOptions, label: copy, buttonClass: '' })); resultArea.append(group);
    }
  }

  function projects(suggestedID = '') {
    const { content, error } = panel('add_project', 'project-manage', 'projects');
    const choices = mountProjectChoices({ content, error, suggestedID, projects: state.projects, request, t, element,
      cancel: closeModal,
      select: async (id, created) => {
        // Creation already grants membership. Joining grants membership only;
        // a folder connection requires a separate CLI authorization.
        const project = created ? null : (await request('/connections', 'POST', {
          request_id: crypto.randomUUID(), schema_version: 1, project_id: id,
        })).project;
        if (!content.isConnected) return;
        history.pushState(null, '', workspaceLocation(id));
        closeModal(); await openWorkspace(state.session);
        if (project) connectionHelp(project, true);
      },
    });
    setModalDismiss(choices.dismiss);
  }
  function renderProjectSettings(container) {
    const project = state.projects.find(project => project.project_id === state.project);
    if (!project) return;
    const content = element('section', undefined, 'project-settings account-content');
    const error = element('p', '', 'account-error'); error.setAttribute('role', 'alert');
    const confirmation = element('div');
    const leave = action('leave_project', () => {
      error.textContent = '';
      const cancel = action('cancel', () => { confirmation.replaceChildren(); controls.hidden = false; error.textContent = ''; leave.focus(); });
      const confirm = action('confirm_leave_project', async () => {
        try { await request('/projects/' + encodeURIComponent(project.project_id) + '/membership', 'DELETE'); if (!content.isConnected) return; closeModal(); await openWorkspace(state.session); }
        catch (caught) { failure(error)(caught); }
      }, 'danger');
      controls.hidden = true;
      confirmation.replaceChildren(element('p', t('leave_project_explanation')), actions(cancel, confirm));
      cancel.focus();
    }, 'danger');
    const controls = actions(leave);
    content.append(controls, confirmation, error); container.append(content);
  }
  function renderProjectFolders(content, projectID) {
    const list = element('div', undefined, 'folder-list');
    const status = element('p', '', 'muted'); status.setAttribute('role', 'status');
    const controls = actions();
    content.append(list, status, controls);
    let folders = [], cursor = '';
    async function load() {
      status.textContent = t('reading'); controls.replaceChildren();
      try {
        const page = await api('/projects/' + encodeURIComponent(projectID) + '/workspaces?limit=100' + (cursor ? '&cursor=' + encodeURIComponent(cursor) : ''), { signal: AbortSignal.timeout(15000) });
        if (!content.isConnected) return;
        folders = [...new Map([...folders, ...page.folders].map(folder => [folder.id, folder])).values()];
        folders.sort((a, b) => a.display_name.localeCompare(b.display_name) || a.device_name.localeCompare(b.device_name) || a.path.localeCompare(b.path));
        list.replaceChildren(...folders.map(folder => {
          const row = element('details', undefined, 'folder-row');
          const summary = element('summary');
          summary.append(element('strong', folder.display_name), element('span', '·', 'muted'), element('span', folder.device_name, 'muted'));
          summary.title = folder.path;
          // Native details also exposes the path on touch devices and with Enter.
          row.append(summary, element('code', folder.path, 'folder-path'));
          return row;
        }));
        cursor = page.next_cursor || '';
        status.textContent = folders.length ? '' : t('folders_empty');
        if (cursor) controls.append(action('load_more', load));
      } catch {
        if (!content.isConnected) return;
        status.textContent = t('folders_failed'); controls.append(action('retry', load));
      }
    }
    load();
  }
  $('provider-login').addEventListener('click', async () => {
    $('provider-login').disabled = true;
    statusMessage('login-error', '');
    try {
      const destination = new URLSearchParams(location.hash.slice(1));
      const result = await request('/v1/auth/start', 'POST', { authorization_id: destination.get('authorize') || '', project_id: parseWorkspaceLocation(location).project });
      const target = new URL(result.url);
      if (target.protocol !== 'https:' && target.hostname !== '127.0.0.1') throw new Error('invalid provider URL');
      location.assign(target.href);
    } catch { statusMessage('login-error', 'login_failed'); $('provider-login').disabled = false; }
  });
  $('account-name').addEventListener('click', settings);
  $('project-manage').addEventListener('click', () => projects());
  $('project-share').addEventListener('click', () => shareProject(state.projects.find(project => project.project_id === state.project), 'project-share'));
  return { loadLogin, afterLogin, renderProjectSettings, refreshLanguage() { if (provider) setButtonLabel($('provider-login'), t('login_with', { provider: provider.label })); } };
}
