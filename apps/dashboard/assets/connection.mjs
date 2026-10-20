import { requestID } from './platform.mjs';
import { disclosure, dialogActions } from './dialog.mjs';
import { createButton } from './button.mjs';
import { icon } from './icons.mjs';
import { isProjectID, projectFromPath } from './catalog.mjs';

export const validProjectName = value => [...value.trim()].length >= 1 && [...value.trim()].length <= 120;

export function sharingScope({ t, element }) {
  const scope = disclosure(t('sharing_scope'), { element, key: 'sharing-scope' });
  scope.body.append(element('p', t('sharing_content'), 'dialog-description'));
  return scope.details;
}

// Membership and directory connection share choices; callers own the final
// operation. Joining a project never implicitly authorizes a device.
export function mountProjectChoices({ content, error, projects = [], suggestedID = '', projectName = '', request,
  includeExisting = false, select, cancel, close = cancel, blocked = () => false, pending = () => false,
  changed = () => {}, t, element }) {
  let saving = false, pendingCreate = null, selected = '', motion = [], transition = 0;
  const active = projects.filter(item => item.status === 'active');
  const nameCounts = new Map();
  for (const item of active) nameCounts.set(item.name, (nameCounts.get(item.name) || 0) + 1);
  const kinds = includeExisting ? ['existing', 'join', 'create'] : ['join', 'create'];
  const root = element('div', undefined, 'project-entry');
  root.dataset.mode = includeExisting ? 'directory' : 'membership';
  const nav = element('div', undefined, 'project-entry-choices');
  nav.setAttribute('role', 'group'); nav.setAttribute('aria-label', t('project_entry_options'));
  const forms = element('div', undefined, 'project-entry-forms'); forms.hidden = true;
  const fields = {}, controls = {}, panels = {}, choices = {};
  const glyphs = { existing: 'folder', join: 'login', create: 'plus' };
  for (const kind of kinds) {
    const choice = element('button', undefined, 'project-entry-choice'); choice.type = 'button'; choice.dataset.choice = kind;
    const caption = element('span', t('project_entry_' + kind)); caption.className = 'project-entry-caption';
    const arrow = icon('chevronRight'); arrow.classList.add('project-entry-arrow');
    choice.append(icon(glyphs[kind]), caption, arrow);
    choice.setAttribute('aria-label', t('project_entry_' + kind));
    choice.addEventListener('click', () => activate(kind)); choices[kind] = choice; nav.append(choice);
    const form = element('form', undefined, 'project-entry-form'); form.dataset.form = kind; form.hidden = true;
    const label = element('label', t(kind === 'existing' ? 'choose_project' : kind === 'join' ? 'shared_project_link' : 'project_name'), 'account-field');
    const input = element(kind === 'existing' ? 'select' : 'input'); input.required = true;
    input.setAttribute('aria-label', t(kind === 'existing' ? 'choose_project' : kind === 'join' ? 'shared_project_link' : 'project_name'));
    if (kind === 'existing') {
      const placeholder = element('option', t('select_project')); placeholder.value = ''; input.append(placeholder);
      for (const item of active) {
        const duplicate = nameCounts.get(item.name) > 1;
        const option = element('option', item.name + (duplicate ? ' · ' + item.project_id : ''));
        option.value = item.project_id; input.append(option);
      }
      if (active.some(item => item.project_id === suggestedID)) input.value = suggestedID;
    } else {
      if (kind === 'join') input.maxLength = 2048;
      input.autocomplete = 'off';
      input.value = kind === 'join' ? suggestedID : projectName;
      if (kind === 'join') { input.spellcheck = false; input.placeholder = t('project_link_placeholder'); }
    }
    const submit = createButton(t(kind === 'existing' ? 'connect_project' : kind === 'join' ? 'join_project' : 'create_project'), {
      glyph: glyphs[kind], className: 'primary', type: 'submit',
    });
    label.append(input); form.append(label, dialogActions(submit));
    input.addEventListener(kind === 'existing' ? 'change' : 'input', update);
    form.addEventListener('submit', event => { event.preventDefault(); if (!submit.disabled) save(kind); });
    fields[kind] = input; controls[kind] = submit; panels[kind] = form; forms.append(form);
  }
  const nameHint = element('p', '', 'project-entry-hint'); nameHint.hidden = true;
  panels.create.insertBefore(nameHint, panels.create.lastElementChild);
  root.append(nav, forms); content.append(root);

  function stopMotion() {
    transition++;
    motion.forEach(animation => animation.cancel()); motion = [];
    root.querySelectorAll('.project-entry-ghost').forEach(node => node.remove());
  }
  async function activate(kind, animate = true) {
    if (saving || blocked() || kind === selected || (kind === 'existing' && !active.length)) return;
    stopMotion();
    const first = !selected;
    const oldWidth = nav.getBoundingClientRect().width;
    const ghost = first && animate ? nav.cloneNode(true) : null;
    selected = kind; root.dataset.selected = kind; forms.hidden = false;
    for (const key of kinds) {
      panels[key].hidden = key !== kind;
      choices[key].setAttribute('aria-pressed', String(key === kind));
      choices[key].querySelector('.project-entry-caption').textContent = t('project_entry_' + key + '_short');
    }
    error.textContent = ''; update();
    const focus = () => { if (content.isConnected) fields[kind].focus({ preventScroll: true }); };
    if (!animate || matchMedia('(prefers-reduced-motion: reduce)').matches) { if (animate) focus(); return; }
    const epoch = transition;
    const options = { duration: 260, easing: 'cubic-bezier(.2,.7,.25,1)' };
    motion.push(forms.animate([{ opacity: 0, transform: 'translateX(24px)' }, { opacity: 1, transform: 'translateX(0)' }], options));
    // A width change folds the choice cards into the navigation. Text is never
    // scaled, and compact layouts retain all choices above the full-width form.
    if (ghost && root.clientWidth >= 420) {
      ghost.classList.add('project-entry-ghost'); ghost.inert = true; ghost.setAttribute('aria-hidden', 'true');
      root.append(ghost);
      motion.push(ghost.animate([{ width: oldWidth + 'px', opacity: 1 }, { width: nav.getBoundingClientRect().width + 'px', opacity: 0 }], options));
      motion.push(nav.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 160, delay: 100, fill: 'backwards' }));
    }
    try { await Promise.all(motion.map(animation => animation.finished)); } catch {}
    if (epoch !== transition) return;
    ghost?.remove(); motion = []; focus();
  }
  async function dismiss() {
    stopMotion();
    // A submitted request can finish after the UI closes. Never apply its late
    // response to another dialog, or pretend closing undoes the server action.
    if (saving || blocked()) { close(); return; }
    saving = true; error.textContent = ''; update(); changed();
    try { await cancel(); }
    catch (caught) { if (content.isConnected) error.textContent = t(connectionErrorKey(caught)); }
    finally { saving = false; update(); changed(); }
  }
  function update() {
    const busy = saving || blocked();
    fields.create.setCustomValidity(validProjectName(fields.create.value) ? '' : t('invalid_project_name'));
    for (const kind of kinds) {
      fields[kind].disabled = busy;
      controls[kind].disabled = busy || pending() || !fields[kind].value.trim();
      choices[kind].disabled = busy || (kind === 'existing' && !active.length);
    }
    if (includeExisting && !active.length) choices.existing.title = t('no_joined_projects');
    const duplicate = active.some(item => item.name.trim().toLowerCase() === fields.create.value.trim().toLowerCase());
    nameHint.textContent = duplicate ? t('project_name_exists') : ''; nameHint.hidden = !duplicate;
  }
  async function save(kind) {
    if (saving || blocked() || pending()) return;
    const newProject = kind === 'create';
    if (newProject && !fields.create.value.trim()) { error.textContent = t('project_name_required'); return; }
    if (newProject && !validProjectName(fields.create.value)) { error.textContent = t('invalid_project_name'); return; }
    let id;
    if (!newProject) {
      try { id = parseProjectInput(fields[kind].value); }
      catch (caught) { error.textContent = t(caught.code); fields[kind].focus(); return; }
    }
    stopMotion(); saving = true; error.textContent = ''; update(); changed();
    const control = controls[kind]; control.setAttribute('aria-busy', 'true');
    try {
      if (newProject) {
        const text = fields.create.value.trim();
        if (!pendingCreate || pendingCreate.name !== text) pendingCreate = { name: text, requestID: 'setup-' + requestID() };
        if (!pendingCreate.id) {
          const result = await request('/projects', 'POST', { schema_version: 1, request_id: pendingCreate.requestID, name: text });
          pendingCreate.id = result.project.project_id;
        }
        id = pendingCreate.id;
      }
      if (content.isConnected) await select(id, newProject);
    } catch (caught) { if (content.isConnected) error.textContent = t(connectionErrorKey(caught)); }
    finally { saving = false; control.removeAttribute('aria-busy'); update(); changed(); }
  }
  update();
  if (suggestedID) activate(includeExisting && fields.existing.value ? 'existing' : 'join', false);
  return { dismiss, update, get busy() { return saving; },
    updateName(previous, next) { if (fields.create.value === previous) fields.create.value = next; update(); } };
}

// Folder access stays on the CLI device through the expiring authorization.
export function mountProjectConnection({ content, error, preview, authorizationID, projects, request, decide, close, t, element }) {
  let current = preview, choosing = false, timer, pathButton;
  const card = element('div', undefined, 'connection-card');
  const folder = element('section', undefined, 'connection-folder');
  card.append(folder); content.append(card);
  const browse = element('button', t('choose_folder'), 'text-button'); browse.type = 'button';
  browse.addEventListener('click', chooseFolder);
  const choices = mountProjectChoices({ content: card, error, projects, includeExisting: true, suggestedID: preview.suggested_project_id || '', projectName: preview.project_name || '', request, close, t, element,
    blocked: () => choosing, pending: () => !!current.directory_pending, changed: update,
    select: async id => {
      try { await decide(true, id, current.directory?.revision); }
      catch (caught) { if (caught?.code === 'directory_changed') await refreshDirectory(); throw caught; }
    },
    cancel: async () => { clearTimeout(timer); try { await decide(false); } finally { scheduleRefresh(); } },
  });
  function update() {
    choices.update();
    browse.disabled = choosing || choices.busy || !!current.directory_pending;
    if (pathButton) pathButton.disabled = browse.disabled;
  }
  async function chooseFolder() {
    if (choosing || choices.busy || current.directory_pending) return;
    choosing = true; error.textContent = ''; update();
    try {
      const next = await request('/authorize', 'POST', { authorization_id: authorizationID, choose_directory: true, directory_revision: current.directory.revision });
      if (!content.isConnected) return;
      current = next; renderFolder(); scheduleRefresh();
    } catch (caught) {
      if (!content.isConnected) return;
      error.textContent = t(connectionErrorKey(caught)); await refreshDirectory();
    } finally { choosing = false; update(); }
  }
  function renderFolder() {
    const directory = current.directory;
    folder.replaceChildren(); pathButton = null;
    if (directory) {
      const line = element('p', undefined, 'connection-folder-line');
      const path = element(directory.can_browse ? 'button' : 'span', directory.path, 'connection-path');
      if (directory.can_browse) {
        pathButton = path; path.type = 'button'; path.classList.add('text-button');
        path.setAttribute('aria-label', t('choose_folder') + ': ' + directory.path);
        path.addEventListener('click', chooseFolder);
      }
      line.append(element('span', t('connecting_folder')), path); folder.append(line);
      if (directory.can_browse) folder.append(browse);
      if (current.directory_pending) folder.append(element('p', t('folder_chooser_pending'), 'muted'));
      if (current.directory_error) folder.append(element('p', t('folder_' + current.directory_error), 'muted'));
    } else folder.append(element('p', t('legacy_folder_help'), 'muted'));
    update();
  }
  function scheduleRefresh() {
    clearTimeout(timer);
    if (current.directory_pending && content.isConnected) timer = setTimeout(refreshDirectory, 2000);
  }
  async function refreshDirectory() {
    if (!content.isConnected) return;
    try {
      const next = await request('/authorize', 'POST', { authorization_id: authorizationID });
      if (!content.isConnected) return;
      choices.updateName(current.project_name || '', next.project_name || '');
      current = next; renderFolder(); scheduleRefresh();
    } catch (caught) {
      if (!content.isConnected) return;
      error.textContent = t(caught?.code === 'authorization_expired' ? 'authorization_expired' : 'account_action_error');
      if (caught?.code !== 'authorization_expired' && caught?.code !== 'authorization_completed') scheduleRefresh();
    }
  }
  renderFolder(); scheduleRefresh(); return choices.dismiss;
}

// POSIX quoting also covers custom private deployment origins. Never interpolate
// an unquoted server/project value into a command intended for a terminal.
export function projectConnectionCommand(projectID, origin) {
  const quote = value => "'" + value.replaceAll("'", "'\\''") + "'";
  const project = projectID ? ' ' + quote(projectID) : '';
  const server = origin === 'https://api.coconet.space' ? '' : ' --server ' + quote(origin);
  return 'coconet connect' + project + server;
}

// A pasted link may identify only this deployment. Never forward browser
// credentials or silently change servers based on user-provided URLs.
export function parseProjectInput(value, origins = [location.origin, document.querySelector('meta[name="coconet-api-origin"]').content]) {
  let id = value.trim();
  if (/^https?:\/\//i.test(id)) {
    let url;
    try { url = new URL(id); } catch { throw Object.assign(new Error('Invalid link'), { code: 'invalid_project_link' }); }
    if (!origins.includes(url.origin) || url.username || url.password || url.search) throw Object.assign(new Error('Wrong project origin'), { code: 'invalid_project_link' });
    id = projectFromPath(url.pathname);
  }
  if (!isProjectID(id)) throw Object.assign(new Error('Invalid Project ID'), { code: 'invalid_project_id' });
  return id;
}

export function connectionErrorKey(error) {
  return ['invalid_project_name', 'project_limit_reached', 'invalid_project_id', 'invalid_project_link', 'project_not_found', 'project_archived', 'project_member_limit_reached', 'authorization_account_mismatch', 'authorization_expired', 'authorization_completed', 'directory_changed'].includes(error?.code) ? error.code : 'account_action_error';
}
