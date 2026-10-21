import { icon } from './icons.mjs';
import { requestID } from './platform.mjs';
import { createProjectNotesState } from './project-notes-state.mjs';
import { renderNoteMarkdown } from './note-markdown.mjs';

const scopes = new Map();
export function mountProjectDocument({ content, project, account, request, element, t, signal }) {
  const key = 'coconet.project-notes.' + document.querySelector('meta[name="coconet-api-origin"]').content + '.' + account + '.' + project;
  let state = scopes.get(key), disposed = false;
  if (!state) {
    let retained = []; try { const data = JSON.parse(sessionStorage.getItem(key)); if (Array.isArray(data)) retained = data; } catch { /* optional storage */ }
    state = createProjectNotesState({ project, account, request, id: requestID, retained, persist: drafts => { try { sessionStorage.setItem(key, JSON.stringify(drafts)); } catch { /* kept in memory */ } } });
    scopes.set(key, state);
  }
  const root = element('div', undefined, 'project-document note-list');
  const hint = element('p', '', 'note-hint');
  const message = element('p', '', 'account-error'); message.setAttribute('role', 'status');
  const add = element('button', undefined, 'note-add'); add.type = 'button';
  const addLabel = element('span'); add.append(icon('plus'), addLabel);
  add.addEventListener('click', () => { const key = state.add(); cards.get(key)?.input.focus(); });
  const cards = new Map();
  root.append(hint, message, add); content.replaceChildren(root);
  const visible = () => !disposed && !document.hidden && content.getClientRects().length;
  const refresh = () => visible() ? state.refresh() : Promise.resolve();
  const resume = () => { if (visible() && navigator.onLine !== false) { refresh(); state.retry(); } };
  const beforeUnload = event => { if (state.unsettled()) { event.preventDefault(); event.returnValue = ''; } };
  const visibility = () => { if (document.hidden) state.flush(); else resume(); };
  window.addEventListener('focus', resume); window.addEventListener('online', resume);
  window.addEventListener('beforeunload', beforeUnload); document.addEventListener('visibilitychange', visibility);
  const timer = setInterval(resume, 60000);
  const unsubscribe = state.subscribe(render);
  signal?.addEventListener('abort', () => {
    state.flush(); disposed = true; unsubscribe(); clearInterval(timer);
    window.removeEventListener('focus', resume); window.removeEventListener('online', resume);
    window.removeEventListener('beforeunload', beforeUnload); document.removeEventListener('visibilitychange', visibility);
  }, { once: true });

  function card(key) {
    const node = element('section', undefined, 'note-bubble'); node.dataset.noteId = key;
    const frame = element('div', undefined, 'note-content');
    const body = element('div', '', 'note-body');
    const input = element('textarea', undefined, 'note-input'); input.rows = 1;
    const remove = element('button', undefined, 'note-remove'); remove.type = 'button'; remove.append(icon('close'));
    const status = element('span', '', 'note-status'); status.setAttribute('role', 'status');
    const issue = element('div', undefined, 'note-issue');
    const issueText = element('p');
    const latest = element('pre', '', 'note-latest');
    const actions = element('div', undefined, 'note-recovery');
    const retry = element('button'), keep = element('button'), discard = element('button');
    for (const button of [retry, keep, discard]) button.type = 'button';
    actions.append(retry, keep, discard); issue.append(issueText, latest, actions);
    frame.append(body, input); node.append(remove, frame, status, issue);
    function begin() { if (state.begin(key)) { input.focus(); input.setSelectionRange(input.value.length, input.value.length); } }
    body.addEventListener('click', () => { if (!window.getSelection()?.toString()) begin(); });
    body.addEventListener('keydown', event => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); begin(); } });
    input.addEventListener('input', () => state.edit(key, input.value));
    input.addEventListener('blur', () => state.end(key));
    remove.addEventListener('pointerdown', event => event.preventDefault());
    remove.addEventListener('click', () => state.remove(key));
    retry.addEventListener('click', () => state.end(key));
    keep.addEventListener('click', () => state.resolve(key, true));
    discard.addEventListener('click', () => state.resolve(key, false));
    let previousText, previousEditing;
    function renderBody(text, editing) {
      const source = editing ? (text || t('note_placeholder')) + '\u200b' : text;
      if (source === previousText && editing === previousEditing) return;
      previousText = source; previousEditing = editing;
      if (editing) body.textContent = source;
      else renderNoteMarkdown(body, source);
    }
    return { node, body, input, remove, status, issue, issueText, latest, retry, keep, discard, renderBody };
  }
  function render() {
    if (disposed || !content.isConnected) return;
    hint.textContent = t('notes_hint');
    message.textContent = state.loadError ? t('notes_load_failed') : state.view.version < 0 ? t('notes_loading') : '';
    message.hidden = !message.textContent;
    add.disabled = state.view.version < 0;
    addLabel.textContent = t('note_publish');
    const ordered = state.view.notes.map(note => note.id);
    for (const key of state.drafts.keys()) if (!ordered.includes(key)) ordered.push(key);
    let previous = message;
    for (const key of ordered) {
      const note = state.view.notes.find(note => note.id === key), draft = state.drafts.get(key);
      const own = draft || note?.author.account_id === account;
      let ui = cards.get(key); if (!ui) { ui = card(key); cards.set(key, ui); }
      if (previous.nextSibling !== ui.node) root.insertBefore(ui.node, previous.nextSibling);
      previous = ui.node;
      ui.node.dataset.owned = String(!!own);
      ui.node.dataset.state = draft?.busy ? 'saving' : draft?.error ? 'error' : draft?.editing ? 'editing' : draft ? 'draft' : 'saved';
      const editing = !!draft?.editing;
      ui.node.dataset.editing = String(editing); ui.input.hidden = !editing;
      ui.body.setAttribute('aria-hidden', String(editing));
      ui.renderBody(draft ? draft.text : note.text, editing);
      ui.body.tabIndex = own && !editing ? 0 : -1;
      if (own) { ui.body.setAttribute('role', 'button'); ui.body.setAttribute('aria-label', t('note_edit') + ': ' + ui.body.textContent); }
      else { ui.body.removeAttribute('role'); ui.body.removeAttribute('aria-label'); }
      if (ui.input.value !== draft?.text && draft) ui.input.value = draft.text;
      ui.input.setAttribute('aria-label', t('note_content')); ui.input.placeholder = t('note_placeholder'); ui.input.disabled = !!draft?.busy;
      ui.remove.hidden = !own; ui.remove.disabled = !!draft?.removing;
      ui.remove.title = t('note_delete'); ui.remove.setAttribute('aria-label', t('note_delete'));
      ui.status.hidden = !draft?.busy; ui.status.title = t('note_saving'); ui.status.setAttribute('aria-label', t('note_saving'));
      if (draft?.busy && !ui.status.firstChild) ui.status.append(icon('refresh'));
      ui.issue.hidden = !draft?.error; ui.issueText.textContent = draft?.error ? t(draft.error) : '';
      const conflict = draft?.error === 'note_conflict', missing = draft?.error === 'note_missing';
      ui.latest.hidden = !conflict; ui.latest.textContent = conflict ? note?.text || '' : ''; ui.latest.setAttribute('aria-label', t('note_latest'));
      ui.retry.hidden = !draft?.error || conflict || missing || draft.error === 'note_not_owned'; ui.retry.textContent = t('retry');
      ui.keep.hidden = !conflict && !missing; ui.keep.textContent = t(missing ? 'note_republish' : 'note_keep');
      ui.discard.hidden = !conflict && !missing; ui.discard.textContent = t('note_discard');
    }
    for (const [key, ui] of cards) if (!ordered.includes(key)) { ui.node.remove(); cards.delete(key); }
    add.hidden = [...state.drafts.values()].some(draft => draft.base === 0 && !draft.error);
  }
  render(); refresh();
  return { render, refresh, flush: () => state.flush() };
}
