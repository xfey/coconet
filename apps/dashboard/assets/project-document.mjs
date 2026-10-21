import { createButton, setButtonLabel } from './button.mjs';
import { dialogActions } from './dialog.mjs';
import { requestID } from './platform.mjs';

// One draft per browser tab, account and project. Closing a panel or refreshing
// the graph never discards unsaved edits; only an explicit save changes the server.
const drafts = new Map();
export function mountProjectDocument({ content, project, account, request, element, t, signal }) {
  const key = 'coconet.project-document.' + document.querySelector('meta[name="coconet-api-origin"]').content + '.' + account + '.' + project;
  let draft = drafts.get(key), current = null, conflict = null, busy = false, errorKey = '', disposed = false;
  let loadEpoch = 0, versionEpoch = 0, historyEpoch = 0;
  let historyBefore = 0, historyStarted = false, historyBusy = false, selectedVersion = null;
  const historyVersions = [];
  if (!draft) { try { draft = JSON.parse(sessionStorage.getItem(key)); } catch { /* tab storage is optional */ } }
  if (!draft || !Number.isSafeInteger(draft.base) || draft.base < 0 || typeof draft.markdown !== 'string' || typeof draft.original !== 'string') draft = null;
  function retain() {
    if (!draft) return;
    drafts.set(key, draft);
    try { sessionStorage.setItem(key, JSON.stringify(draft)); } catch { /* retain in memory */ }
  }
  const endpoint = '/projects/' + encodeURIComponent(project) + '/document';
  const root = element('div', undefined, 'dialog-stack project-document');
  const description = element('p', '', 'dialog-description');
  const metadata = element('p', '', 'dialog-description'); metadata.setAttribute('role', 'status');
  const field = element('label', undefined, 'dialog-section');
  const label = element('span');
  const input = element('textarea', undefined, 'document-editor'); input.spellcheck = false; input.rows = 12;
  field.append(label, input);
  const error = element('p', '', 'account-error'); error.setAttribute('role', 'alert');
  const save = createButton('', { glyph: 'check', className: 'primary', onClick: () => saveDraft() });
  const reload = createButton('', { glyph: 'refresh', onClick: () => refresh() });
  const comparison = element('section', undefined, 'dialog-section document-comparison');
  const comparisonTitle = element('h3'); const latestText = element('pre', '', 'document-source');
  comparison.append(comparisonTitle, latestText);
  const history = element('section', undefined, 'dialog-section document-history');
  const historyTitle = element('h3');
  const historyBody = element('div', undefined, 'dialog-section');
  const list = element('div', undefined, 'document-versions');
  const more = createButton('', { glyph: 'chevronDown', onClick: () => loadHistory() });
  const oldText = element('pre', '', 'document-source');
  const restore = createButton('', { glyph: 'refresh', onClick: () => restoreVersion() });
  const historyError = element('p', '', 'account-error'); historyError.setAttribute('role', 'alert');
  historyBody.append(list, more, oldText, dialogActions(restore), historyError); history.append(historyTitle, historyBody);
  root.append(description, metadata, field, error, comparison, dialogActions(reload, save), history);
  content.replaceChildren(root);
  const alive = () => !disposed && content.isConnected;
  const dirty = () => draft && draft.markdown !== draft.original;
  const beforeUnload = event => { if (dirty() || busy) { event.preventDefault(); event.returnValue = ''; } };
  window.addEventListener('beforeunload', beforeUnload);
  signal?.addEventListener('abort', () => { disposed = true; retain(); window.removeEventListener('beforeunload', beforeUnload); }, { once: true });

  function render() {
    if (!alive()) return;
    description.textContent = t('document_intro'); label.textContent = t('document_markdown');
    metadata.textContent = current?.version ? t('document_saved_version', { version: current.version, author: current.author.display_name, time: new Date(current.saved_at).toLocaleString() }) : t('document_unsaved');
    if (dirty()) metadata.textContent += ' · ' + t('document_draft');
    if (input.value !== (draft?.markdown || '')) input.value = draft?.markdown || '';
    input.disabled = busy || !draft;
    error.textContent = errorKey ? t(errorKey) : '';
    save.disabled = busy || !draft || (!dirty() && !conflict);
    save.setAttribute('aria-busy', String(busy));
    reload.disabled = busy;
    setButtonLabel(save, t(conflict ? 'document_save_merge' : 'document_save'));
    setButtonLabel(reload, t('refresh'));
    comparison.hidden = !conflict;
    comparisonTitle.textContent = t('document_latest', { version: conflict?.version || 0 });
    latestText.textContent = conflict?.markdown || t('document_empty');
    historyTitle.textContent = t('document_history');
    setButtonLabel(more, t('load_more')); more.hidden = !historyBefore; more.disabled = historyBusy;
    oldText.hidden = !selectedVersion; oldText.textContent = selectedVersion?.markdown || t('document_empty');
    restore.hidden = !selectedVersion; restore.disabled = busy || !!dirty() || !!conflict;
    setButtonLabel(restore, t('document_restore', { version: selectedVersion?.version || 0 }));
    restore.title = dirty() ? t('document_restore_draft') : '';
    list.replaceChildren(...historyVersions.map(version => createButton(t('document_version', { version: version.version, author: version.author.display_name }), {
      glyph: 'read', onClick: async () => {
        const epoch = ++versionEpoch;
        try { const result = await request(endpoint + '/versions/' + version.version, { signal }); if (alive() && epoch === versionEpoch) { selectedVersion = result; historyError.textContent = ''; render(); } }
        catch (e) { if (alive() && epoch === versionEpoch && e.name !== 'AbortError') historyError.textContent = t('document_load_failed'); }
      },
    })));
  }
  async function refresh() {
    const epoch = ++loadEpoch;
    try {
      const result = await request(endpoint, { signal });
      if (!alive() || epoch !== loadEpoch) return;
      errorKey = ''; current = result;
      if (!draft || !dirty() && !draft.pending) { draft = { base: result.version, markdown: result.markdown, original: result.markdown }; conflict = null; }
      else if (draft.base !== result.version) { conflict = result; errorKey = 'document_conflict'; }
      retain(); render();
    } catch (e) { if (alive() && epoch === loadEpoch && e.name !== 'AbortError') { errorKey = 'document_load_failed'; render(); } }
  }
  async function saveDraft() {
    if (!draft || busy) return;
    if (new TextEncoder().encode(draft.markdown).length > 256 * 1024) { errorKey = 'document_too_large'; render(); return; }
    const base = conflict ? conflict.version : draft.base;
    if (!draft.pending || draft.pending.markdown !== draft.markdown) {
      draft.pending = { schema_version: 1, request_id: requestID(), base_version: base, markdown: draft.markdown };
    }
    const pending = draft.pending, submitted = draft;
    loadEpoch++;
    retain(); busy = true; errorKey = ''; render();
    try {
      // A closed panel may still receive the save acknowledgement. Update this
      // scoped draft without touching a later panel or issuing a second save.
      const result = await request(endpoint, { method: 'PUT', body: JSON.stringify(pending) });
      current = result; conflict = null;
      if (drafts.get(key) === submitted && submitted.markdown === pending.markdown) { draft = { base: result.version, markdown: result.markdown, original: result.markdown }; retain(); }
      historyEpoch++; versionEpoch++; historyBusy = false;
      historyVersions.length = 0; historyBefore = 0; historyStarted = false; selectedVersion = null;
      if (alive()) await loadHistory();
    } catch (e) {
      if (e.code === 'document_conflict') { delete draft.pending; retain(); errorKey = 'document_conflict'; if (alive()) await refresh(); }
      else errorKey = 'document_save_failed';
    } finally { busy = false; render(); }
  }
  async function loadHistory() {
    if (historyBusy) return;
    const epoch = ++historyEpoch;
    historyBusy = true; render();
    try {
      const page = await request(endpoint + '/history' + (historyBefore && historyStarted ? '?before=' + historyBefore : ''), { signal });
      if (!alive() || epoch !== historyEpoch) return;
      if (!historyStarted) historyVersions.length = 0;
      historyVersions.push(...page.versions); historyBefore = page.next_before || 0; historyStarted = true; historyError.textContent = '';
    } catch (e) { if (alive() && epoch === historyEpoch && e.name !== 'AbortError') historyError.textContent = t('document_load_failed'); }
    finally { if (epoch === historyEpoch) { historyBusy = false; render(); } }
  }
  function restoreVersion() {
    if (!selectedVersion || dirty() || conflict || busy || !current) return;
    draft = { base: current.version, markdown: selectedVersion.markdown, original: current.markdown };
    retain(); render(); input.focus();
  }
  input.addEventListener('input', () => { if (draft) { draft.markdown = input.value; retain(); render(); } });
  render(); refresh(); loadHistory();
  return { render, refresh };
}
