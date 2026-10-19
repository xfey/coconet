import { createPublishedGraphReader, publishedGraphStatus, publishedSessionActions, renderPublishedStatus } from "./published-graph.mjs";
import { createProjectEvents } from "./events.mjs";
import { renderInstallation } from './installation.mjs';
import { dialogActions } from './dialog.mjs';
import { createAccountUI } from "./account.mjs";
import { createLibraryActions } from './library.mjs';
import { emptyLayout, edgePath, nodeWidth, nodeHeight, shortIdentity, sourceKey, sourceIdentity } from './graph.mjs';

import { workspaceLocation, parseWorkspaceLocation } from './catalog.mjs';

import { createNotifications } from './notifications.mjs';
import { createCanvas } from './canvas.mjs';
import { createLayoutClient } from './layout-client.mjs';
import { createButton, styleButton, setButtonLabel } from './button.mjs';
import { icon } from './icons.mjs';
import { detectLanguage, translate, errorKey, languageStorageKey } from './i18n.mjs';

const $ = id => document.getElementById(id);
for (const control of document.querySelectorAll('[data-icon]')) control.replaceChildren(icon(control.dataset.icon));
for (const control of document.querySelectorAll('[data-button-icon]')) styleButton(control, { glyph: control.dataset.buttonIcon });
const state = { projects: [], project: '', nodes: [], selected: '', graphEpoch: 0, zoom: 1, session: null };
Object.assign(state, { layout: emptyLayout(), navigationEpoch: 0, selection: null, selectionEpoch: 0 });
let modal = null, modalEpoch = 0, copyEpoch = 0;
const graphLayout = createLayoutClient();
let savedLanguage;
try { savedLanguage = localStorage.getItem(languageStorageKey); } catch { /* restricted storage still permits in-memory selection */ }
let language = detectLanguage(savedLanguage, navigator.languages || [navigator.language]);
const t = (key, values) => translate(language, key, values);
const libraryActions = createLibraryActions({
  request: (project, input) => api('/projects/' + encodeURIComponent(project) + '/library-selection', { method: 'POST', body: JSON.stringify(input), signal: AbortSignal.timeout(15000) }),
  accepted: () => refreshWorkspace({ preserveViewport: true }),
  changed: () => renderDetails(),
});
const projectEvents = createProjectEvents({ refresh: () => refreshWorkspace({ preserveViewport: true }), revoked: () => openWorkspace(state.session) });
const notifications = createNotifications({ region: $('notifications'), viewport: $('graph-viewport'), details: $('details'), library: $('library-open'), translate: t });
function applyStaticLanguage() {
  document.documentElement.lang = language === 'zh' ? 'zh-CN' : 'en';
  for (const node of document.querySelectorAll('[data-i18n]')) {
    if (node.classList.contains('ui-button')) setButtonLabel(node, t(node.dataset.i18n));
    else node.textContent = t(node.dataset.i18n);
  }
  for (const attribute of ['aria-label', 'title', 'placeholder']) for (const node of document.querySelectorAll('[data-i18n-' + attribute + ']')) node.setAttribute(attribute, t(node.getAttribute('data-i18n-' + attribute)));
  for (const node of document.querySelectorAll('[data-language]')) node.value = language;
  for (const node of document.querySelectorAll('[data-message-key]')) node.textContent = node.dataset.messageKey ? t(node.dataset.messageKey) : '';
  updateZoomTitle();
}
function changeLanguage(next) {
  language = detectLanguage(next);
  try { localStorage.setItem(languageStorageKey, language); } catch { /* no persistent storage available */ }
  applyStaticLanguage(); notifications.refreshLanguage(); accountUI.refreshLanguage(); renderProjects(); renderGraph(); renderLibrary(); renderDetails();
  if (modal?.kind === 'action') renderAction();
  else if (modal?.kind === 'installation') renderInstall();
  else if (modal?.kind === 'projection') { $('modal-title').textContent = t('sync_status'); modal.ui.render(); }
}
function statusMessage(id, key) {
  const node = $(id); node.dataset.messageKey = key; node.textContent = key ? t(key) : ''; node.hidden = !key;
}
function setGraphRefreshing(refreshing) {
  const control = $('refresh'), key = refreshing ? 'refreshing' : 'refresh';
  control.disabled = refreshing || !state.session || !state.project;
  control.setAttribute('aria-busy', String(refreshing));
  for (const attribute of ['aria-label', 'title']) {
    control.setAttribute('data-i18n-' + attribute, key);
    control.setAttribute(attribute, t(key));
  }
  $('graph-viewport').setAttribute('aria-busy', String(refreshing));
}
function updateZoomTitle() { $('zoom-fit').title = t('fit_zoom', { percent: Math.round(state.zoom * 100) }); }

function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
}
function button(text, action, className) {
  const node = element('button', text, className);
  node.type = 'button'; node.addEventListener('click', action); return node;
}
function formatTime(value, dateOnly = false) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? t('time_unknown') : date.toLocaleString(language === 'zh' ? 'zh-CN' : 'en', { month: '2-digit', day: '2-digit', ...(dateOnly ? {} : { hour: '2-digit', minute: '2-digit', hour12: false }) });
}
async function api(path, options = {}) {
  const response = await fetch(path.startsWith('/v1/') ? path : '/v1/dashboard' + path, { credentials: 'same-origin', cache: 'no-store', ...options, headers: { 'Content-Type': 'application/json', ...options.headers } });
  if (response.status === 204) return null;
  const data = await response.json();
  if (!response.ok) {
    const error = new Error(data.code || 'error_generic');
    error.code = data.code; error.status = response.status; throw error;
  }
  return data;
}
function clearWorkspace() {
  closeModal(false);
  state.project = ''; resetProject();
  state.graphEpoch++;
  state.nodes = []; state.projects = []; state.project = ''; state.selected = ''; state.session = null;
  $('project-list').replaceChildren(); $('graph-nodes').replaceChildren(); $('library-nodes').replaceChildren(); $('detail-content').replaceChildren();
  $('account-label').textContent = ''; notice('');
  $('workspace').hidden = true; $('identity').hidden = true; $('login').hidden = false; $('loading').hidden = true;
}
function handleError(error) {
  if (error.status === 401) { clearWorkspace(); statusMessage('login-error', errorKey(error)); return; }
  if (error.status === 403) resetProject();
  notice(errorKey(error));
}
function notice(key) {
  notifications.notice(key);
  notifications.action(['published_graph_waiting', 'published_graph_behind', 'published_graph_processing', 'published_graph_sessions_pending', 'published_graph_relations_pending', 'published_graph_invalid', 'graph_refresh_error', 'published_graph_unknown'].includes(key) ? 'view_sync_status' : '', () => openProjection());
}
function openProjection(trigger = 'notice') {
  openModal('projection', t('sync_status'), trigger);
  const render = () => {
    if (state.published) renderPublishedStatus(state.published, { element, t, button, copyText, selectNode, content: $('modal-content'), readError: state.graphReadError });
    else $('modal-content').replaceChildren(element('p', t(state.graphReadError || 'published_graph_unknown'), 'dialog-description'));
  };
  modal.ui = { render }; render();
}
function toast(key) { notifications.toast(key); }
function setModalDismiss(dismiss) { if (modal) modal.dismiss = dismiss; }
function dismissModal() { if (modal?.dismiss) return modal.dismiss(); closeModal(); }
function closeModal(restoreFocus = true) {
  if (!modal) return;
  const trigger = modal.trigger;
  modal = null; modalEpoch++;
  $('session-modal').close(); $('modal-content').replaceChildren();
  notifications.clearToast();
  notifications.modal(false);
  if (restoreFocus) {
    const target = trigger === 'notice' ? $('notice').querySelector('button') || $('canvas-settings') : $(trigger);
    target?.focus({ preventScroll: true });
  }
}
// Account surfaces remain open while background navigation restores a source.
function closeProjectModal() {
  if (['settings', 'profile', 'projects'].includes(modal?.kind) || modal?.kind === 'share' && modal.project === state.project) return;
  closeModal(false);
}
function openModal(kind, title, trigger) {
  closeModal(false);
  modal = { kind, trigger, project: state.project }; modalEpoch++;
  $('session-modal').dataset.kind = kind;
  $('modal-title').textContent = title;
  $('modal-content').scrollTop = 0;
  $('session-modal').showModal();
  notifications.modal(true);
}
function openAction(action, trigger) {
  openModal('action', t(action.labelKey), trigger);
  modal.action = action; renderAction(); copyText(action.text);
}
function renderAction() {
  const action = modal.action, content = $('modal-content'), scroll = content.scrollTop;
  $('modal-title').textContent = t(action.labelKey);
  const pre = element('pre', undefined, 'instruction'); pre.append(element('code', action.text));
  const footer = element('div', undefined, 'dialog-actions copy-footer');
  const copy = actionButton(t('copy_instruction'), 'copy', () => copyText(action.text), 'primary');
  footer.append(element('p', t('copy_fallback'), 'muted'), copy);
  content.replaceChildren(element('p', t(action.descriptionKey), 'modal-description'), pre, footer); content.scrollTop = scroll;
}
function openInstallation({ project, trigger = 'install-open' } = {}) {
  openModal('installation', t('install_title'), trigger);
  modal.installation = { project }; renderInstall();
}
function renderInstall() {
  $('modal-title').textContent = t('install_title');
  renderInstallation({ content: $('modal-content'), ...modal.installation, apiOrigin: document.querySelector('meta[name="coconet-api-origin"]').content, t, element, copyText });
}
function openSettings() { openModal('settings', t('settings'), 'canvas-settings'); renderSettings(); }
function renderSettings() {
  $('modal-title').textContent = t('settings');
  const label = element('label', undefined, 'preferences-language');
  label.append(element('span', t('language_label')));
  const select = element('select');
  select.dataset.language = ''; select.setAttribute('aria-label', t('language'));
  for (const [value, text] of [['zh', '中文'], ['en', 'English']]) {
    const option = element('option', text); option.value = value; select.append(option);
  }
  select.value = language; label.append(select);
  const content = element('div', undefined, 'dialog-stack settings-content');
  content.append(label);
  accountUI.renderProjectSettings(content);
  const version = element('div', undefined, 'settings-version');
  version.append(element('span', t('server_version')), element('span', document.querySelector('meta[name="coconet-server-version"]').content));
  content.append(version); $('modal-content').replaceChildren(content);
}
async function openWorkspace(session) {
  state.session = session; $('account-key').value = ''; statusMessage('login-error', '');
  $('login').hidden = true; $('loading').hidden = true; $('workspace').hidden = false; $('identity').hidden = false;
  $('account-label').textContent = session.account.display_name;
  const epoch = ++state.graphEpoch;
  try {
    const response = await api('/projects');
    if (epoch !== state.graphEpoch) return;
    state.projects = response.projects;
    if (!state.projects.length) {
      state.project = ''; resetProject(); renderProjects(); document.title = 'Coconet';
      notice('');
      setGraphRefreshing(false); return;
    }
    const target = parseWorkspaceLocation(location);
    if (target.project && !state.projects.some(project => project.project_id === target.project)) {
      state.project = ''; resetProject(); renderProjects(); document.title = 'Coconet'; return;
    }
    state.project = state.projects.some(project => project.project_id === target.project) ? target.project : state.projects[0].project_id;
    resetProject(); renderProjects();
    await refreshWorkspace();
    await restoreLocation(target);
  } catch (error) { if (epoch === state.graphEpoch) handleError(error); }
}
const publishedGraphReader = createPublishedGraphReader();
async function refreshGraph({ preserveViewport = false } = {}) {
  if (!state.session || !state.project) return false;
  const epoch = ++state.graphEpoch, project = state.project;
  setGraphRefreshing(true);
  $('graph-empty').hidden = true;
  document.title = (state.projects.find(item => item.project_id === project)?.name || t('projects')) + ' · Coconet';
  try {
    const graph = await publishedGraphReader.read(project);
    if (epoch !== state.graphEpoch) return false;
    const layout = await graphLayout.layout(graph.nodes);
    if (epoch !== state.graphEpoch) return false;
    state.published = graph;
    state.graphReadError = '';
    state.revision = graph.event_revision;
    $('library-open').hidden = !state.project;

    projectEvents.acknowledge(project, state.revision);
    const layoutChanged = state.layout !== layout;
    const previousIDs = new Set(state.nodes.map(node => node.id));
    const addedNodes = graph.nodes.some(node => !previousIDs.has(node.id));
    state.nodes = graph.nodes; state.layout = layout;
    const selected = [...graph.nodes, ...graph.library].find(node => node.id === state.selected);
    if (!selected && !state.selection) state.selected = '';
    if (state.selection?.graph && selected && sourceKey(selected) === sourceKey(state.selection.node)) state.selection.node = { ...selected };
    if (state.selection?.graph && !selected) { state.selection = null; state.selected = ''; updateLocation(); }
    notice(graph.warning);
    if (modal?.kind === 'projection') modal.ui.render();
    renderGraph(); renderLibrary(); renderDetails();
    if ((!preserveViewport && addedNodes) || state.needsFit && state.nodes.length) { canvas.fit(); state.needsFit = false; }
    else if (!preserveViewport && layoutChanged) revealSelection();
    return true;
  } catch (error) {
    if (epoch !== state.graphEpoch) return false;
    if (error.status === 404) resetProject();
    handleError(error);
    if (![401, 403, 404].includes(error.status)) {
      state.graphReadError = error.message === 'published_graph_invalid' ? 'published_graph_invalid' : state.published ? 'graph_refresh_error' : 'published_graph_unknown';
      notice(state.graphReadError);
      if (modal?.kind === 'projection') modal.ui.render();
    }
    return false;
  } finally { if (epoch === state.graphEpoch) { setGraphRefreshing(false); renderGraphEmpty(); } }
}
function resetProject() {
  libraryActions.reset();
  state.graphReadError = '';
  publishedGraphReader.reset();
  projectEvents.stop(); state.revision = 0; state.published = null;
  closeProjectModal(); toggleLibraryDrawer(false);
  state.navigationEpoch++;
  setGraphRefreshing(false);
  state.graphEpoch++; state.selectionEpoch++;
  graphLayout.reset(); state.layout = emptyLayout();
  state.needsFit = true;
  state.nodes = []; state.selected = ''; state.selection = null;
  canvas.reset(); renderLibrary();
  renderGraph(); renderDetails();
}
function selectedNode() { return state.selection?.node || state.nodes.find(node => node.id === state.selected); }
function selectedSource() { return state.selection?.source || selectedNode()?.source; }
function detailKey() { const source = selectedSource(); return source ? sourceIdentity(source) + ':' + source.version : ''; }
function linkSelection() {
  const source = selectedSource();
  return source ? { ...(state.selected ? { node: state.selected } : {}), session: sourceIdentity(source), version: String(source.version) } : {};
}
function updateLocation(replace = false) {
  const target = workspaceLocation(state.project, 'graph', linkSelection());
  if (location.pathname + location.hash === target && !location.search) return;
  history[replace ? 'replaceState' : 'pushState'](null, '', target);
}
function renderProjects() {
  if (modal?.kind === 'settings') renderSettings();
  const project = state.projects.find(item => item.project_id === state.project);
  $('project-share').disabled = !project;
  $('library-open').hidden = !project;
  $('project-list').classList.toggle('projects-empty', !state.projects.length);
  if (!state.projects.length) {
    $('project-list').replaceChildren(element('p', t('no_projects')), element('p', t('join_project_hint')));
    return;
  }
  $('project-list').replaceChildren(...state.projects.map(project => {
    const item = button('', () => switchProject(project.project_id), 'project-item');
    item.dataset.projectId = project.project_id;
    item.setAttribute('aria-current', project.project_id === state.project ? 'page' : 'false');
    const mark = icon('folder');
    item.append(mark, element('span', project.name, 'project-name'));
    if (project.status === 'archived') item.append(element('span', t('archived'), 'archived'));
    item.title = project.name;
    return item;
  }));
}
async function switchProject(id) {
  if (state.project === id) return;
  state.project = id; resetProject(); renderProjects(); updateLocation(); await refreshWorkspace();
}
function closeSelection(focus = false, update = true) {
  closeProjectModal();
  const id = state.selected;
  state.selectionEpoch++; state.navigationEpoch++;
  state.selected = ''; state.selection = null;

  renderGraph(); renderLibrary(); renderDetails();
  if (update) updateLocation();
  if (focus) {
    const node = [...document.querySelectorAll('.work-node')].find(card => card.dataset.nodeId === id);
    (node || $('graph-viewport')).focus({ preventScroll: true });
  }
}
function selectNode(id, replaceLocation = false) {
  const node = state.nodes.find(item => item.id === id) || state.published?.library.find(item => item.id === id);
  if (!node) return;
  selectRecord({ node, source: node.source, graph: true }, replaceLocation);
}
function selectRecord(selection, replaceLocation = false) {
  closeProjectModal(); toggleLibraryDrawer(false);
  state.selectionEpoch++;
  state.selection = selection; state.selected = selection.node?.id || '';
  state.navigationEpoch++; updateLocation(replaceLocation); renderGraph(); renderLibrary(); renderDetails();
  revealSelection();
  $('detail-close').focus({ preventScroll: true });
}
function revealSelection() {
  if (!state.selected) return;
  const card = [...document.querySelectorAll('.work-node')].find(card => card.dataset.nodeId === state.selected);
  if (card) canvas.reveal({ x: parseFloat(card.style.left), y: parseFloat(card.style.top), width: nodeWidth, height: nodeHeight }, $('graph-viewport').clientWidth >= 800 ? $('details').offsetWidth + 32 : 0);
}
const canvas = createCanvas({
  viewport: $('graph-viewport'), world: $('graph'),
  rightInset: () => !$('details').hidden && $('graph-viewport').clientWidth - $('details').offsetWidth >= 160 ? $('details').offsetWidth : 0,
  onZoom: zoom => { state.zoom = zoom; updateZoomTitle(); },
  onBackground: () => closeSelection(),
});
async function restoreLocation(target = parseWorkspaceLocation(location)) {
  if (!state.session) return;
  let navigation = ++state.navigationEpoch;
  if (target.project && target.project !== state.project) {
    if (!state.projects.some(project => project.project_id === target.project)) { await accountUI.afterLogin(); return; }
    state.project = target.project; resetProject(); renderProjects(); navigation = state.navigationEpoch; await refreshWorkspace();
  }
  if (!state.session || navigation !== state.navigationEpoch || target.project && state.project !== target.project) return;
  if (target.event || target.entry) { notice('link_node_unavailable'); return; }
  if (target.node || target.session) {
    const node = state.nodes.find(node => node.id === target.node) || state.published?.library.find(node => node.id === target.node);
    if (node && (!target.session || sourceIdentity(node.source) === target.session && node.source.version === target.version)) selectNode(node.id, true);
    else { closeSelection(false, false); notice('link_node_unavailable'); }
  } else closeSelection(false, false);
}
function renderGraphEmpty() {
  $('graph-empty').textContent = state.nodes.length ? '' : t(publishedGraphStatus(state.published, { readError: state.graphReadError }).summary);
  $('graph-empty').hidden = !state.project || state.nodes.length > 0 || $('graph-viewport').getAttribute('aria-busy') === 'true';
}
function renderGraph() {
  const layout = state.layout, byID = new Map(state.nodes.map(node => [node.id, node]));
  const activeID = document.activeElement?.classList.contains('work-node') ? document.activeElement.dataset.nodeId : null;
  renderGraphEmpty();
  $('graph-nodes').replaceChildren();
  $('graph').style.width = layout.width + 'px'; $('graph').style.height = layout.height + 'px';
  canvas.setBounds(layout);
  const namespace = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(namespace, 'svg'); svg.classList.add('edges');
  svg.setAttribute('width', layout.width); svg.setAttribute('height', layout.height); svg.setAttribute('aria-hidden', 'true');
  const defs = document.createElementNS(namespace, 'defs'), marker = document.createElementNS(namespace, 'marker');
  marker.id = 'arrow'; marker.setAttribute('viewBox', '0 0 10 10'); marker.setAttribute('refX', '9'); marker.setAttribute('refY', '5'); marker.setAttribute('markerWidth', '6'); marker.setAttribute('markerHeight', '6'); marker.setAttribute('orient', 'auto');
  const arrow = document.createElementNS(namespace, 'path'); arrow.setAttribute('d', 'M 0 0 L 10 5 L 0 10 z'); arrow.setAttribute('fill', 'context-stroke'); marker.append(arrow); defs.append(marker); svg.append(defs);
  for (const { parent, child, points } of layout.edges) {
    const path = document.createElementNS(namespace, 'path');
    path.setAttribute('d', edgePath(points));
    path.setAttribute('class', 'edge' + (state.selected === child || state.selected === parent ? ' active' : ''));
    path.dataset.parent = parent; path.dataset.child = child;
    path.setAttribute('marker-end', 'url(#arrow)'); svg.append(path);
  }
  $('graph-nodes').append(svg);
  for (const id of layout.order) {
    const node = byID.get(id), position = layout.positions.get(id);
    const card = button('', () => selectNode(id), 'work-node ' + node.state + (id === state.selected ? ' selected' : ''));
    card.dataset.nodeId = id; card.setAttribute('aria-pressed', String(id === state.selected));
    card.setAttribute('aria-label', `${node.objective}，${t(node.state)}，${shortIdentity(node.author)}`);
    card.style.left = position.x + 'px'; card.style.top = position.y + 'px';
    card.addEventListener('focus', () => canvas.reveal({ ...position, width: nodeWidth, height: nodeHeight }));
    const meta = element('span', undefined, 'node-meta');
    meta.append(element('span', node.author.display_name, 'node-author'), element('span', formatTime(node.work_time.end, true), 'node-date'));
    card.append(element('strong', node.objective), meta);
    $('graph-nodes').append(card);
    if (activeID === id) card.focus({ preventScroll: true });
  }
}
function toggleLibraryDrawer(open, restoreFocus = false) {
  $('library-drawer').hidden = !open;
  $('library-open').setAttribute('aria-expanded', String(open));
  if (open) $('library-close').focus({ preventScroll: true });
  else if (restoreFocus) $('library-open').focus({ preventScroll: true });
}
function renderLibrary() {
  const list = $('library-nodes'), scroll = list.scrollTop, activeID = document.activeElement?.dataset.nodeId;
  list.replaceChildren();
  const nodes = state.published?.library || [];
  $('library-count').textContent = nodes.length || '';
  statusMessage('library-message', nodes.length ? '' : 'library_empty');
  $('library-drawer').setAttribute('aria-busy', 'false');
  for (const node of nodes) {
    const card = button('', () => { toggleLibraryDrawer(false); selectNode(node.id); }, 'library-entry' + (state.selected === node.id ? ' selected' : ''));
    card.dataset.nodeId = node.id; card.setAttribute('aria-pressed', String(state.selected === node.id));
    card.append(icon('star'), element('strong', node.objective), element('span', node.author.display_name, 'node-meta')); list.append(card);
    if (activeID === node.id && !$('library-drawer').hidden) card.focus({ preventScroll: true });
  }
  list.scrollTop = scroll;
}
async function copyText(text) {
  const epoch = modalEpoch, attempt = ++copyEpoch;
  try { await navigator.clipboard.writeText(text); if (epoch === modalEpoch && attempt === copyEpoch) toast('copy_success'); }
  catch { if (epoch === modalEpoch && attempt === copyEpoch) toast('copy_failed'); }
}
function actionButton(text, glyph, action, className) {
  return createButton(text, { glyph, onClick: action, className, size: className === 'session-action' ? 'session' : 'regular' });
}
async function changeLibrary(node) {
  const project = state.project, epoch = libraryActions.epoch;
  const relevant = () => project === state.project && epoch === libraryActions.epoch;
  try {
    if (!await libraryActions.change(project, node) || !relevant()) return;
    // The controller refreshes before unlocking, instead of toggling locally:
    // a concurrent Agent operation may already supersede this receipt.
    const latest = [...(state.published?.nodes || []), ...(state.published?.library || [])].find(item => item.id === node.id);
    if (state.graphReadError) toast('library_updated_refresh_pending');
    else if (!!latest?.library === !node.library) toast(node.library ? 'library_removed' : 'library_saved');
    else toast('library_updated');
  } catch (error) {
    if (!relevant()) return;
    if (error.status === 401) { handleError(error); return; }
    if (['library_selection_changed', 'library_owner_only', 'project_archived'].includes(error.code)) await refreshWorkspace({ preserveViewport: true });
    if (relevant()) toast(['library_selection_changed', 'library_owner_only', 'project_archived', 'library_busy'].includes(error.code) ? error.code : 'library_update_failed');
  }
}
function renderDetails() {
  const content = $('detail-content'), node = selectedNode(), source = selectedSource(), key = detailKey();
  const focused = content.contains(document.activeElement) ? document.activeElement.id : '';
  const scrollTop = content.dataset.sourceKey === key ? content.scrollTop : 0;
  content.dataset.sourceKey = key; content.replaceChildren();
  $('details').hidden = !source;
  notifications.position();
  if (!source) return;
  content.append(element('h3', node?.objective || t('session')));
  if (node) content.append(element('p', node.author.display_name + ' · ' + formatTime(node.work_time.end), 'detail-meta'));

  const actions = element('div', undefined, 'detail-actions');
  const availableActions = node ? publishedSessionActions(state.project, node, { collect: node.author.account_id === state.session?.account?.account_id && state.projects.find(project => project.project_id === state.project)?.status === 'active' }) : [];
  for (const [index, action] of availableActions.entries()) {
    const id = 'session-action-' + index;
    const collection = action.kind === 'library';
    const busy = collection && libraryActions.pending(state.project, node);
    const control = actionButton(t(busy ? 'saving' : action.labelKey), action.icon, () => collection ? changeLibrary(node) : openAction(action, id), 'session-action');
    control.id = id;
    if (collection) { control.disabled = busy; control.setAttribute('aria-busy', String(busy)); control.setAttribute('aria-pressed', String(!!node.library)); }
    else control.setAttribute('aria-haspopup', 'dialog');
    actions.append(control);
  }
  content.append(actions);
  for (const [field, label] of [['summary', 'work_summary'], ['approach', 'work_approach']]) {
    if (!node?.[field]) continue;
    const section = element('section', undefined, 'work-description');
    const heading = element('h4', t(label)); heading.id = 'detail-' + field + '-heading';
    section.setAttribute('aria-labelledby', heading.id);
    section.append(heading, element('p', node[field])); content.append(section);
  }
  if (state.published && node) {
    if (node.relations !== 'ready') content.append(element('p', t('published_relation_' + node.relations), 'detail-meta'));
    if (node.library?.note) content.append(element('p', node.library.note, 'detail-meta'));
  }
  if (state.selection?.graph && node && state.nodes.some(item => item.id === node.id && sourceKey(item) !== sourceKey(node))) content.append(createButton(t('view_latest'), { glyph: 'refresh', onClick: () => selectNode(node.id) }));
  content.scrollTop = scrollTop;
  if (focused) document.getElementById(focused)?.focus({ preventScroll: true });
}
const accountUI = createAccountUI({state, api, t, element, statusMessage, openModal, closeModal, setModalDismiss, openWorkspace, clearWorkspace, toast, openInstallation});

$('login-form').addEventListener('submit', async event => {
  event.preventDefault(); $('login-button').disabled = true; statusMessage('login-error', '');
  const key = $('account-key').value.trim(); $('account-key').value = '';
  try { await openWorkspace(await api('/session', { method: 'POST', body: JSON.stringify({ schema_version: 1, account_key: key }) })); await accountUI.afterLogin(); }
  catch (error) { statusMessage('login-error', errorKey(error)); }
  finally { $('login-button').disabled = false; }
});
async function refreshWorkspace(options = {}) {
  const project = state.project;
  const refreshed = await refreshGraph(options);
  if (state.project === project && state.needsFit && state.nodes.length) { canvas.fit(); state.needsFit = false; }
  if (state.project === project && state.session && !document.hidden) {
    projectEvents.start(project, state.revision);
  }
  return refreshed;
}
// History traversal and same-document fragment navigation both emit popstate.
// Listening to hashchange too would race two restores against one graph read.
window.addEventListener('popstate', () => { if (new URLSearchParams(location.hash.slice(1)).has('authorize')) accountUI.afterLogin(); else restoreLocation(); });
$('refresh').addEventListener('click', refreshWorkspace);
$('detail-close').addEventListener('click', () => closeSelection(true));
$('modal-close').addEventListener('click', dismissModal);
$('session-modal').addEventListener('cancel', event => { event.preventDefault(); dismissModal(); });
$('session-modal').addEventListener('keydown', event => {
  if (event.key !== 'Tab') return;
  const controls = [...$('session-modal').querySelectorAll('button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), a[href], summary, [tabindex="0"]')].filter(node => node.checkVisibility());
  const target = event.shiftKey ? controls.at(-1) : controls[0];
  if (document.activeElement === (event.shiftKey ? controls[0] : controls.at(-1))) {
    event.preventDefault(); target.focus();
  }
});
let backdropDown = false;
const outsideModal = event => {
  const rect = $('session-modal').getBoundingClientRect();
  return event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom;
};
$('session-modal').addEventListener('pointerdown', event => { backdropDown = event.target === $('session-modal') && outsideModal(event); });
$('session-modal').addEventListener('click', event => { if (backdropDown && event.target === $('session-modal') && outsideModal(event)) closeModal(); backdropDown = false; });
$('canvas-settings').addEventListener('click', openSettings);
$('install-open').addEventListener('click', () => openInstallation({ project: state.projects.find(item => item.project_id === state.project && item.status === 'active') }));
$('login-install').addEventListener('click', () => openInstallation({ trigger: 'login-install' }));
$('library-open').addEventListener('click', () => toggleLibraryDrawer($('library-drawer').hidden));
$('library-close').addEventListener('click', () => toggleLibraryDrawer(false, true));
document.addEventListener('pointerdown', event => {
  if (!event.target.closest('#library-drawer, #library-open') && !$('session-modal').open) toggleLibraryDrawer(false);
});
document.addEventListener('change', event => {
  if (!event.target.matches('[data-language]')) return;
  changeLanguage(event.target.value);
  if (modal?.kind === 'settings') $('modal-content').querySelector('[data-language]').focus();
});

document.addEventListener('keydown', event => {
  if (event.key !== 'Escape' || $('session-modal').open) return;
  if (!$('library-drawer').hidden) toggleLibraryDrawer(false, true);
  else if (selectedSource()) closeSelection(true);
});
$('zoom-in').addEventListener('click', () => canvas.step(.1));
$('zoom-out').addEventListener('click', () => canvas.step(-.1));
$('zoom-fit').addEventListener('click', () => canvas.fit());
function pauseConnection() { projectEvents.stop(); }
function resumeConnection() {
  pauseConnection();
  if (document.hidden) return;
  const streaming = !!(state.session && state.project && navigator.onLine !== false);
  if (streaming) projectEvents.start(state.project, state.revision);
}
document.addEventListener('visibilitychange', resumeConnection);
window.addEventListener('pagehide', pauseConnection);
window.addEventListener('pageshow', resumeConnection);
window.addEventListener('online', resumeConnection);
window.addEventListener('offline', resumeConnection);
applyStaticLanguage();
try { await accountUI.loadLogin(); await openWorkspace(await api('/session')); await accountUI.afterLogin(); }
catch (error) { clearWorkspace(); if (error.status !== 401) statusMessage('login-error', errorKey(error)); }
