// Per-note drafts and idempotent mutations; independent of the DOM so a save
// can finish safely while its drawer is closed or another project is open.
export function createProjectNotesState({ project, account, request, id, retained = [], persist = () => {} }) {
  const endpoint = '/projects/' + encodeURIComponent(project) + '/document';
  const drafts = new Map();
  for (const item of retained) {
    if (typeof item.id === 'string' && typeof item.text === 'string' && typeof item.original === 'string' && Number.isSafeInteger(item.base) && item.base >= 0) {
      drafts.set(item.id, { ...item, busy: false, editing: false });
    }
  }
  const listeners = new Set();
  let view = { version: -1, notes: [] }, loadError = false, loading = null;
  function emit() {
    persist([...drafts.values()].map(({ busy, editing, ...draft }) => draft));
    for (const listener of listeners) listener();
  }
  function accept(result) { if (result.version >= view.version) view = result; }
  const note = key => view.notes.find(item => item.id === key);
  function begin(key) {
    let draft = drafts.get(key), original = note(key);
    if (draft?.busy || draft?.removing || !draft && original?.author.account_id !== account) return false;
    if (!draft) {
      draft = { id: key, base: original.revision, text: original.text, original: original.text };
      drafts.set(key, draft);
    }
    draft.editing = true; draft.ready = false; emit(); return true;
  }
  function add() {
    if (view.version < 0) return;
    const existing = [...drafts.values()].find(draft => draft.base === 0 && !draft.pending && !draft.removing);
    const key = existing?.id || id();
    if (!existing) drafts.set(key, { id: key, base: 0, text: '', original: '', editing: true });
    else existing.editing = true;
    emit(); return key;
  }
  function edit(key, text) {
    const draft = drafts.get(key);
    if (!draft || !draft.editing || draft.busy) return;
    draft.text = text;
    if (draft.error === 'note_empty' || draft.error === 'notes_too_large') draft.error = '';
    emit();
  }
  async function refresh() {
    if (loading) return loading;
    loading = (async () => {
      try { accept(await request(endpoint)); loadError = false; }
      catch { loadError = true; }
      finally { loading = null; emit(); }
    })();
    return loading;
  }
  async function save(key) {
    const draft = drafts.get(key);
    if (!draft || draft.busy || draft.editing || ['note_conflict', 'note_missing', 'note_not_owned'].includes(draft.error)) return;
    if (!draft.pending) {
      if (draft.removing && draft.base === 0 || !draft.removing && draft.text === draft.original) { drafts.delete(key); emit(); return; }
      if (!draft.removing && !draft.text.trim()) {
        if (draft.base === 0) drafts.delete(key); else draft.error = 'note_empty';
        emit(); return;
      }
      if (new TextEncoder().encode(draft.text).length > 256 * 1024) { draft.error = 'notes_too_large'; emit(); return; }
      draft.pending = { method: draft.removing ? 'DELETE' : 'PUT', body: { schema_version: 1, request_id: id(), base_revision: draft.base, text: draft.removing ? '' : draft.text } };
    }
    const pending = draft.pending;
    draft.busy = true; draft.error = ''; emit();
    try {
      const result = await request(endpoint + '/notes/' + encodeURIComponent(key), { method: pending.method, body: JSON.stringify(pending.body) });
      accept(result);
      draft.pending = null;
      if (pending.method === 'DELETE') drafts.delete(key);
      else {
        const saved = note(key);
        if (!saved) { if (draft.removing) drafts.delete(key); else draft.error = 'note_missing'; }
        else {
          draft.base = saved.revision; draft.original = pending.body.text;
          if (saved.text !== pending.body.text && (draft.text !== pending.body.text || draft.removing)) draft.error = 'note_conflict';
          // A replay returns the current view; never overwrite a newer edit
          // from another device with the contents of an old acknowledgement.
          if (draft.text === pending.body.text && !draft.removing) drafts.delete(key);
        }
      }
    } catch (error) {
      const actionable = ['note_conflict', 'note_missing', 'note_not_owned', 'notes_too_large', 'invalid_note_content'];
      draft.error = actionable.includes(error.code) ? error.code === 'invalid_note_content' ? 'note_empty' : error.code : 'note_save_failed';
      if (actionable.includes(error.code)) draft.pending = null;
      if (error.code === 'note_conflict' || error.code === 'note_missing') await refresh();
    } finally { draft.busy = false; emit(); }
    if (drafts.has(key) && !draft.error && !draft.editing) return save(key);
  }
  function end(key) {
    const draft = drafts.get(key); if (!draft) return Promise.resolve();
    draft.editing = false; draft.ready = true; emit();
    return save(key);
  }
  function remove(key) {
    let draft = drafts.get(key), original = note(key);
    if (!draft && original?.author.account_id !== account) return Promise.resolve();
    if (!draft) { draft = { id: key, base: original.revision, text: original.text, original: original.text }; drafts.set(key, draft); }
    if (draft.error === 'note_missing') { drafts.delete(key); emit(); return Promise.resolve(); }
    draft.removing = true; draft.editing = false; draft.ready = true; draft.error = ''; emit();
    return save(key);
  }
  function resolve(key, keep) {
    const draft = drafts.get(key); if (!draft || draft.busy) return;
    const latest = note(key);
    if (!keep) { drafts.delete(key); emit(); return; }
    if (!latest) {
      drafts.delete(key); draft.id = id(); draft.base = 0; draft.original = ''; drafts.set(draft.id, draft);
    } else { draft.base = latest.revision; draft.original = latest.text; }
    draft.pending = null; draft.error = ''; draft.removing = false; draft.editing = false; draft.ready = true;
    return save(draft.id);
  }
  function flush() { return Promise.all([...drafts.keys()].map(end)); }
  function retry() { return Promise.all([...drafts.values()].filter(draft => draft.ready && !draft.editing && draft.error === 'note_save_failed').map(draft => save(draft.id))); }
  return {
    get view() { return view; }, get loadError() { return loadError; }, drafts,
    subscribe(listener) { listeners.add(listener); return () => listeners.delete(listener); },
    add, begin, edit, end, remove, resolve, refresh, flush, retry,
    unsettled: () => [...drafts.values()].some(draft => draft.pending || draft.removing || draft.text !== draft.original),
  };
}
