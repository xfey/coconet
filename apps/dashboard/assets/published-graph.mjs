import { sha256 } from './platform.mjs';
import { graphStructure } from './graph.mjs';
import { commandBlock, disclosure } from './dialog.mjs';

const validID = value => typeof value === 'string' && /^[A-Za-z0-9_.-]{1,200}$/.test(value);
const integer = value => Number.isSafeInteger(value) && value >= 0;
const record = value => value && typeof value === 'object' && !Array.isArray(value);
const own = (value, key) => Object.hasOwn(value, key);
const fail = () => { throw new Error('published_graph_invalid'); };
async function verifiedJSON(encoded, digest, maxBytes) {
  if (typeof encoded !== 'string' || encoded.length > Math.ceil(maxBytes / 3) * 4 || !/^[a-f0-9]{64}$/.test(digest)) fail();
  let bytes;
  try { bytes = Uint8Array.from(atob(encoded), c => c.charCodeAt(0)); } catch { fail(); }
  if (bytes.length > maxBytes) fail();
  const hash = await sha256(bytes);
  if (hash !== digest) fail();
  try { return JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes)); } catch { fail(); }
}

// Read-only projection of a member-generated checkpoint. No transcript fetch,
// model call or second implementation of the DAG reducer runs in the browser.
export async function publishedGraph(response, project) {
  const page = response?.relay;
  if (response?.schema_version !== 1 || page?.schema_version !== 1 || !integer(page.head) || !integer(page.generation) || !integer(response.event_revision)) fail();
  const empty = { nodes: [], revision: 0, event_revision: response.event_revision, head: page.head, pending: 0, relations: 0, sessions: [], library: [], published: true, warning: 'published_graph_waiting' };
  if (!page.head) { if (page.checkpoint !== null) fail(); return { ...empty, warning: '' }; }
  if (page.checkpoint === null) return empty;
  if (!record(page.checkpoint)) fail();
  const receipt = page.checkpoint, cp = receipt.checkpoint, anchor = receipt.anchor;
  if (receipt.schema_version !== 1 || receipt.project_id !== project || cp?.schema_version !== 1 || !['coconet-state-json-v1', 'coconet-state-json-v2', 'coconet-state-json-v3'].includes(cp.codec) || !integer(cp.revision) || cp.revision < 1 || cp.revision > page.head || anchor?.schema_version !== 1 || anchor.project_id !== project || anchor.revision !== cp.revision || anchor.commit?.schema_version !== 1 || !['coconet-json-v2', 'coconet-json-v3', 'coconet-json-v4', 'coconet-json-v5'].includes(anchor.commit.codec) || anchor.commit.base + 1 !== cp.revision || anchor.commit.id !== cp.commit_id || anchor.commit.state_sha256 !== cp.sha256) fail();
  const [state, commit] = await Promise.all([verifiedJSON(cp.payload, cp.sha256, 16 << 20), verifiedJSON(anchor.commit.payload, anchor.commit.sha256, 1 << 20)]);
  if (!record(state) || !record(commit)) fail();
  if (commit.project_id !== project || commit.id !== cp.commit_id || commit.actor !== anchor.writer || commit.base + 1 !== cp.revision || state.schema !== 1 || state.project_id !== project || state.revision !== cp.revision || !['nodes', 'sessions', 'snapshots', 'library'].every(key => record(state[key]))) fail();
  if (JSON.stringify(commit.processing || null) !== JSON.stringify(anchor.commit.processing || null)) fail();
  const result = { ...empty, revision: cp.revision, warning: cp.revision < page.head ? 'published_graph_behind' : '', writer: receipt.writer, accepted_at: receipt.accepted_at };
  const sourceExists = source => validID(source?.session_id) && integer(source.version) && source.version > 0 && own(state.snapshots, `${source.session_id}/${source.version}`);
  for (const [id, session] of Object.entries(state.sessions)) {
    if (!record(session)) fail();
    if (!validID(id) || session.id !== id || !validID(session.owner) || !integer(session.latest) || !integer(session.processed_through) || session.processed_through > session.latest || !Array.isArray(session.items) || session.items.length > 256 || session.items.some(item => !record(item) || !validID(item.id) || typeof item.text !== 'string')) fail();
    if (session.processed_through < session.latest) result.pending++;
    result.sessions.push({ id, owner: session.owner, items: session.items, objective: own(state.nodes, session.tail || '') && record(state.nodes[session.tail]) ? state.nodes[session.tail].objective : '' });
  }
  for (const [id, node] of Object.entries(state.nodes)) {
    if (!record(node)) fail();
    if (!validID(id) || node.id !== id || !sourceExists(node.source) || !own(state.sessions, node.source.session_id) || typeof node.objective !== 'string' || !node.objective.trim() || node.objective.length > 1200 || ['summary', 'approach'].some(key => node[key] !== undefined && (typeof node[key] !== 'string' || node[key].length > 8000)) || !['current', 'sealed', 'withdrawn'].includes(node.state) || !['pending', 'needs_context', 'ready'].includes(node.relations) || !Array.isArray(node.parents) || !Number.isFinite(Date.parse(node.updated_at))) fail();
    const session = state.sessions[node.source.session_id];
    const snapshot = state.snapshots[`${node.source.session_id}/${node.source.version}`];
    if (!record(snapshot)) fail();
    if (snapshot.session_id !== node.source.session_id || snapshot.version !== node.source.version || snapshot.owner !== session.owner || node.source.version > session.processed_through) fail();
    for (const edge of node.parents) {
      if (!record(edge)) fail();
      if (!own(state.nodes, edge.parent) || !sourceExists(edge.source)) fail();
      const parent = state.nodes[edge.parent];
      if (!record(parent) || !record(parent.source)) fail();
      if (parent.state !== 'sealed' || edge.source.session_id !== parent.source.session_id || edge.source.version !== parent.source.version) fail();
    }
    if (node.state === 'withdrawn' && (!own(state.library, id) || node.parents.length)) fail();
    if (node.state !== 'withdrawn' && snapshot.superseded) fail();
    if (node.state !== 'withdrawn' && node.relations !== 'ready') result.relations++;
    const ownerAccount = session.owner.split('.')[0];
    result.nodes.push({ id, objective: node.objective, summary: node.summary || '', approach: node.approach || '', source: node.source, revision: node.revision, state: node.state, agent: session.agent, relations: node.relations, parents: node.parents.map(edge => edge.parent), author: { account_id: ownerAccount, display_name: typeof response.authors?.[ownerAccount] === 'string' ? response.authors[ownerAccount] : (session.agent || ownerAccount) }, owner: session.owner, work_time: { end: node.updated_at }, ...(own(state.library, id) ? { library: { note: state.library[id] } } : {}) });
  }
  for (const [id, note] of Object.entries(state.library)) { if (!own(state.nodes, id) || typeof note !== 'string' || note.length > 8000) fail(); }
  try { graphStructure(result.nodes); } catch { fail(); }
  result.nodes.sort((a, b) => a.id.localeCompare(b.id));
  result.library = result.nodes.filter(node => node.library);
  result.nodes = result.nodes.filter(node => node.state !== 'withdrawn');
  // These are reading leads, never inferred completion of a Session's items.
  // Include references to any stage in the Session, not just its current tail.
  const nodesByID = new Map(result.nodes.map(node => [node.id, node]));
  const followUps = new Map();
  for (const node of result.nodes) {
    for (const parentID of node.parents) {
      const sourceSession = nodesByID.get(parentID)?.source.session_id;
      if (!sourceSession || sourceSession === node.source.session_id) continue;
      if (!followUps.has(sourceSession)) followUps.set(sourceSession, new Set());
      followUps.get(sourceSession).add(node.id);
    }
  }
  for (const session of result.sessions) session.followUps = [...(followUps.get(session.id) || [])].sort().map(id => nodesByID.get(id));
  if (!result.warning && (result.pending || result.relations)) result.warning = result.pending && result.relations ? 'published_graph_processing' : result.pending ? 'published_graph_sessions_pending' : 'published_graph_relations_pending';
  return result;
}

export function publishedSessionActions(project, node, { collect = false } = {}) {
  const selection = `Project ID: ${project}\n${JSON.stringify({ work_node_id: node.id, source: node.source }, null, 2)}`;
  const actions = [{ labelKey: 'read_action', icon: 'read', descriptionKey: 'read_description', text: `Use Coconet to read the shared work below. Call pull_session with the exact selection below, then read and search the returned compact_path for context relevant to our current task. Keep this fixed source; do not silently substitute another version. If the selection is stale or unavailable, explain it and refresh the work graph. Distinguish the session's claims from verified facts.\n\n${selection}` }];
  if (['codex', 'claude', 'cursor'].includes(node.agent)) actions.push({ labelKey: 'continue_action', icon: 'continue', descriptionKey: 'continue_description', text: `Use Coconet to continue from this exact ${node.agent} source in the current Agent (Codex, Claude or Cursor). Cross-Agent continuation transfers user/assistant text only; tools, reasoning and media are not converted. Read its compact transcript with pull_session, then call fork_session with a unique request_id and the exact selection below. Reuse that request_id and all original arguments on retry. Preserve source history and current workspace files. Return the new native Session ID, working directory and complete resume command. If the selected version or compatible Agent is unavailable, explain the failure.\n\n${selection}` });
  if (collect) actions.push({ kind: 'library', labelKey: node.library ? 'library_remove' : 'library_add', icon: 'star' });
  return actions;
}

// Only synced facts are available here. Editable nodes and open work items do
// not imply a processing failure, and pending work does not prove device liveness.
export function publishedGraphStatus(graph, { readError = '' } = {}) {
  if (readError) return { summary: readError, next: 'published_next_refresh' };
  if (!graph) return { summary: 'published_graph_unknown', next: '' };
  if (!graph.head) return { summary: 'published_graph_empty', next: 'published_next_empty' };
  if (graph.warning) return { summary: graph.warning, next: 'published_next_pending' };
  if (!graph.nodes.length) return { summary: 'published_graph_no_nodes', next: 'published_next_empty' };
  return { summary: 'published_graph_ready', next: '' };
}

export function renderPublishedStatus(graph, { element, t, content, button, copyText, selectNode, readError = '' }) {
  const scroll = content.scrollTop;
  const expanded = content.querySelector('[data-disclosure="published-sync"]')?.open || false;
  const expandedItems = content.querySelector('[data-disclosure="session-items"]')?.open || false;
  const expandedFollowUps = new Set([...content.querySelectorAll('[data-disclosure^="session-follow-ups-"][open]')].map(node => node.dataset.disclosure));
  const focused = content.contains(document.activeElement) ? document.activeElement : null;
  const disclosureKey = focused?.closest('summary')?.parentElement?.dataset.disclosure;
  const followUpID = focused?.closest('[data-follow-up-node]')?.dataset.followUpNode;
  const focusTarget = disclosureKey ? `[data-disclosure="${disclosureKey}"] summary` : followUpID ? `[data-follow-up-node="${followUpID}"]` : focused?.closest('.command-block button') ? '.command-block button' : '';
  const stack = element('div', undefined, 'dialog-stack');
  const section = element('section', undefined, 'dialog-section');
  const status = publishedGraphStatus(graph, { readError });
  const countsOnly = !readError && ['published_graph_processing', 'published_graph_sessions_pending', 'published_graph_relations_pending'].includes(status.summary);
  if (!countsOnly) section.append(element('p', t(status.summary), 'dialog-lead'));
  if (readError) section.append(element('p', t('published_graph_cached'), 'dialog-description'));
  const counts = [];
  if (graph.pending) counts.push(t('published_pending_conversations', { count: graph.pending }));
  if (graph.relations) counts.push(t('published_pending_relations', { count: graph.relations }));
  if (counts.length) section.append(element('p', counts.join(' · '), countsOnly ? 'dialog-lead' : 'dialog-description'));
  stack.append(section);
  const sync = disclosure(t('published_sync_details'), { element, key: 'published-sync', opened: expanded });
  sync.body.append(element('p', t('published_run_in_folder'), 'dialog-description'));
  sync.body.append(commandBlock('coconet inference status\ncoconet health --json\ncoconet diagnostics list', { element, t, copyText, buttonClass: '' }));
  sync.body.append(element('p', `${t('published_revision')}: ${graph.revision} / ${graph.head}`, 'dialog-description'));
  stack.append(sync.details);
  const items = graph.sessions.flatMap(session => session.items);
  if (items.length) {
    const notes = disclosure(t('published_open_items_count', { count: items.length }), { element, key: 'session-items', opened: expandedItems });
    const section = notes.body;
    section.append(element('p', t('published_open_items_scope'), 'dialog-description'));
    for (const session of graph.sessions.filter(session => session.items.length)) {
      const group = element('div', undefined, 'dialog-section');
      if (session.objective) group.append(element('h4', session.objective));
      for (const item of session.items) group.append(element('p', item.text, 'dialog-description'));
      if (session.followUps?.length && selectNode) {
        const key = `session-follow-ups-${session.id}`;
        const followUps = disclosure(t('published_follow_ups'), { element, key, opened: expandedFollowUps.has(key) });
        for (const node of session.followUps) {
          const control = button(node.objective, () => selectNode(node.id), 'session-follow-up');
          control.dataset.followUpNode = node.id;
          followUps.body.append(control);
        }
        group.append(followUps.details);
      }
      section.append(group);
    }
    stack.append(notes.details);
  }
  content.replaceChildren(stack);
  content.scrollTop = scroll;
  if (focusTarget) content.querySelector(focusTarget)?.focus({ preventScroll: true });
}



// One validated graph for the active workspace only. Reset invalidates requests
// already in flight, so a late response cannot populate another account's cache.
export function createPublishedGraphReader(fetcher = fetch) {
  let cached = null, epoch = 0;
  return {
    reset() { cached = null; epoch++; },
    async read(project) {
      const request = ++epoch, previous = cached?.project === project ? cached : null;
      const response = await fetcher(`/v1/dashboard/projects/${encodeURIComponent(project)}/published-graph`, {
        credentials: 'same-origin', cache: 'no-store', headers: previous?.etag ? { 'If-None-Match': previous.etag } : {},
      });
      if (response.status === 304) {
        if (!previous) fail();
        return previous.graph;
      }
      if (!response.ok) {
        if (request === epoch) cached = null;
        const data = await response.json();
        const error = new Error(data.code || 'error_generic');
        error.code = data.code; error.status = response.status; throw error;
      }
      // Clear before decoding; a rejected response must be re-fetched in full.
      if (request === epoch) cached = null;
      const graph = await publishedGraph(await response.json(), project);
      if (request === epoch) cached = { project, etag: response.headers.get('ETag'), graph };
      return graph;
    },
  };
}
