import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { createPublishedGraphReader, publishedGraph, publishedGraphStatus, publishedSessionActions } from './assets/published-graph.mjs';

function envelope(state, head = state.revision) {
  const payload = JSON.stringify(state), sha256 = createHash('sha256').update(payload).digest('hex');
  const commit = JSON.stringify({ id: 'commit', project_id: 'p', actor: 'a.d', base: state.revision - 1 });
  return { schema_version: 1, event_revision: head + 1, authors: { a: '同事' }, relay: { schema_version: 1, generation: head, head, checkpoint: { schema_version: 1, project_id: 'p', writer: 'a.d', checkpoint: { schema_version: 1, codec: 'coconet-state-json-v1', revision: state.revision, commit_id: 'commit', payload: Buffer.from(payload).toString('base64'), sha256 }, anchor: { schema_version: 1, project_id: 'p', revision: state.revision, writer: 'a.d', commit: { schema_version: 1, id: 'commit', codec: 'coconet-json-v2', base: state.revision - 1, payload: Buffer.from(commit).toString('base64'), sha256: createHash('sha256').update(commit).digest('hex'), state_sha256: sha256 } } } } };
}
function fixture() {
  return { schema: 1, project_id: 'p', revision: 2, sessions: { s: { id: 's', owner: 'a.d', agent: 'claude', latest: 2, processed_through: 1, items: [{ id: 'todo', text: '之后验证' }] } }, snapshots: { 's/1': { session_id: 's', version: 1, owner: 'a.d' } }, nodes: { n: { id: 'n', objective: '理解，先完善重试。', summary: '<img src=x onerror=alert(1)>', source: { session_id: 's', version: 1 }, revision: 1, state: 'current', relations: 'pending', parents: [], updated_at: '2026-10-09T00:00:00Z' } }, library: {} };
}
test('hosted projection preserves fixed source, pending work and literal text', async () => {
  const graph = await publishedGraph(envelope(fixture(), 3), 'p');
  assert.equal(graph.warning, 'published_graph_behind');
  assert.equal(graph.pending, 1); assert.equal(graph.relations, 1);
  assert.equal(graph.nodes[0].author.display_name, '同事');
  assert.equal(graph.nodes[0].summary, '<img src=x onerror=alert(1)>');
  assert.equal(graph.nodes[0].source.server_session_id, undefined);
  const actions = publishedSessionActions('p', graph.nodes[0]);
  assert.equal(actions.length, 2); assert.match(actions[0].text, /"session_id": "s"/);
  assert.doesNotMatch(actions[0].text, /fork_session|server_session_id/);
  assert.match(actions[1].text, /fork_session/); assert.match(actions[1].text, /current Agent \(Codex, Claude or Cursor\)/);
  assert.doesNotMatch(actions[1].text, /pull_id|server_session_id/);
  const owned = publishedSessionActions('p', graph.nodes[0], { collect: true });
  assert.equal(owned.length, 3); assert.equal(owned[2].kind, 'library'); assert.equal(owned[2].text, undefined);
  const collected = publishedSessionActions('p', {...graph.nodes[0], library: {note: ''}}, {collect: true});
  assert.equal(collected.length, 3); assert.equal(collected[2].kind, 'library'); assert.equal(collected[2].text, undefined);
  assert.equal(collected[2].labelKey, 'library_remove');
});

test('cross-session follow-ups are reading leads and never complete historical items', async () => {
  const state = fixture();
  state.nodes.n.state = 'sealed';
  state.sessions.s.tail = 'tail';
  state.nodes.tail = { ...state.nodes.n, id: 'tail', objective: 'Unrelated later stage', parents: [] };
  state.sessions.other = { id: 'other', owner: 'b.d', agent: 'codex', latest: 1, processed_through: 1, items: [] };
  state.snapshots['other/1'] = { session_id: 'other', version: 1, owner: 'b.d' };
  state.nodes.child = { ...state.nodes.n, id: 'child', objective: 'Partial follow-up, not necessarily completion', source: { session_id: 'other', version: 1 }, parents: [{ parent: 'n', source: state.nodes.n.source }, { parent: 'tail', source: state.nodes.tail.source }] };
  const graph = await publishedGraph(envelope(state), 'p');
  const session = graph.sessions.find(value => value.id === 's');
  assert.deepEqual(session.followUps.map(node => node.id), ['child'], 'deduplicated across every stage, not only the tail');
  assert.deepEqual(session.items, state.sessions.s.items, 'reference must not resolve an item');
  assert.equal(session.followUps[0].source.session_id, 'other');
  state.nodes.child.state = 'withdrawn'; state.nodes.child.parents = []; state.library.child = 'Retained historical source';
  const after = await publishedGraph(envelope(state), 'p');
  assert.deepEqual(after.sessions.find(value => value.id === 's').followUps, [], 'withdrawal must remove the reading lead');
  assert.deepEqual(after.sessions.find(value => value.id === 's').items, state.sessions.s.items);
});
test('empty Relay stays in the local workflow while missing checkpoints report waiting', async () => {
  const empty = await publishedGraph({ schema_version: 1, event_revision: 1, relay: { schema_version: 1, head: 0, generation: 0, checkpoint: null } }, 'p');
  assert.equal(empty.published, true); assert.equal(empty.warning, ''); assert.deepEqual(empty.nodes, []);
  const graph = await publishedGraph({ schema_version: 1, event_revision: 2, relay: { schema_version: 1, head: 1, generation: 1, checkpoint: null } }, 'p');
  assert.equal(graph.warning, 'published_graph_waiting'); assert.deepEqual(graph.nodes, []);
});
test('pending notice separates conversation processing from relation analysis and clears on completion', async () => {
  const state = fixture();
  assert.equal((await publishedGraph(envelope(state), 'p')).warning, 'published_graph_processing');
  state.nodes.n.relations = 'ready';
  assert.equal((await publishedGraph(envelope(state), 'p')).warning, 'published_graph_sessions_pending');
  state.sessions.s.processed_through = state.sessions.s.latest;
  assert.equal((await publishedGraph(envelope(state), 'p')).warning, '');
  for (const relation of ['pending', 'needs_context']) {
    state.nodes.n.relations = relation;
    const graph = await publishedGraph(envelope(state), 'p');
    assert.equal(graph.pending, 0);
    assert.equal(graph.warning, 'published_graph_relations_pending');
    assert.equal((await publishedGraph(envelope(state, 3), 'p')).warning, 'published_graph_behind');
  }
});
test('status distinguishes absent uploads, unpublished work, processed empty work and editable ready nodes', async () => {
  assert.equal(publishedGraphStatus(null).summary, 'published_graph_unknown');
  const response = { schema_version: 1, event_revision: 1, relay: { schema_version: 1, head: 0, generation: 0, checkpoint: null } };
  assert.equal(publishedGraphStatus(await publishedGraph(response, 'p')).summary, 'published_graph_empty');
  response.relay.head = 1;
  assert.equal(publishedGraphStatus(await publishedGraph(response, 'p')).summary, 'published_graph_waiting');
  const state = fixture();
  state.nodes.n.relations = 'ready';
  state.sessions.s.processed_through = state.sessions.s.latest;
  // An editable node and business open items do not make processing pending.
  assert.equal(publishedGraphStatus(await publishedGraph(envelope(state), 'p')).summary, 'published_graph_ready');
  const ready = await publishedGraph(envelope(state), 'p');
  assert.equal(publishedGraphStatus(ready).next, '', 'ready work needs no next-step instruction');
  assert.equal(publishedGraphStatus(ready, { readError: 'graph_refresh_error' }).summary, 'graph_refresh_error');
  assert.equal(publishedGraphStatus(ready).summary, 'published_graph_ready');
  state.nodes = {};
  assert.equal(publishedGraphStatus(await publishedGraph(envelope(state), 'p')).summary, 'published_graph_no_nodes');
  assert.equal(publishedGraphStatus(await publishedGraph(envelope(state, 3), 'p')).summary, 'published_graph_behind');
  state.sessions.s.latest++;
  assert.equal(publishedGraphStatus(await publishedGraph(envelope(state), 'p')).summary, 'published_graph_sessions_pending');
});
test('corruption, foreign identity, broken anchor, invalid source and cycles fail closed', async () => {
  const corrupt = envelope(fixture()); corrupt.relay.checkpoint.checkpoint.payload = Buffer.from('{}').toString('base64');
  await assert.rejects(publishedGraph(corrupt, 'p'));
  await assert.rejects(publishedGraph(envelope(fixture()), 'other'));
  const anchor = envelope(fixture()); anchor.relay.checkpoint.anchor.commit.state_sha256 = '0'.repeat(64);
  await assert.rejects(publishedGraph(anchor, 'p'));
  for (const mutate of [s => { s.sessions.s = null; }, s => { s.nodes.n = null; }, s => { s.nodes.n.source.version = 42; }, s => { s.nodes.n.state = 'sealed'; s.nodes.n.parents = [{ parent: 'n', source: s.nodes.n.source }]; }, s => { s.sessions.s.processed_through = 3; }, s => { s.library.missing = 'note'; }]) {
    const state = fixture(); mutate(state);
    await assert.rejects(publishedGraph(envelope(state), 'p'));
  }
});

test('published graph reads sparse retained sources without offering retired versions', async () => {
  const state = fixture();
  state.sessions.s.latest = state.sessions.s.processed_through = 50;
  state.nodes.n.source.version = 50;
  state.snapshots = { 's/50': { session_id: 's', version: 50, owner: 'a.d' } };
  const graph = await publishedGraph(envelope(state), 'p');
  assert.equal(graph.pending, 0);
  assert.equal(graph.nodes[0].source.version, 50);
  assert.match(publishedSessionActions('p', graph.nodes[0])[0].text, /"version": 50/);
  state.nodes.n.source.version = 3;
  await assert.rejects(publishedGraph(envelope(state), 'p'));
});

test('conditional reader caches only verified current workspace and clears rejected responses', async () => {
  const requests = [], responses = [];
  const reader = createPublishedGraphReader(async (url, options) => { requests.push({url, ...options}); return responses.shift(); });
  const good = () => new Response(JSON.stringify(envelope(fixture())), {headers: {ETag:'"v1"'}});
  responses.push(good()); const first = await reader.read('p');
  responses.push(new Response(null, {status:304})); assert.equal(await reader.read('p'), first);
  assert.equal(requests.at(-1).headers['If-None-Match'], '"v1"');
  responses.push(new Response('{"code":"membership_revoked"}', {status:403}));
  await assert.rejects(reader.read('p'), e => e.status === 403);
  responses.push(good()); await reader.read('p'); assert.deepEqual(requests.at(-1).headers, {});
  const bad = envelope(fixture()); bad.relay.checkpoint.checkpoint.sha256 = '0'.repeat(64);
  responses.push(new Response(JSON.stringify(bad), {headers:{ETag:'"bad"'}}));
  await assert.rejects(reader.read('p'));
  responses.push(good()); await reader.read('p'); assert.deepEqual(requests.at(-1).headers, {});
  reader.reset(); responses.push(good()); await reader.read('p'); assert.deepEqual(requests.at(-1).headers, {});
});
test('late conditional response cannot repopulate a reset workspace', async () => {
  let release, headers;
  const reader = createPublishedGraphReader((url, options) => { headers = options.headers; return new Promise(resolve => { release = resolve; }); });
  const old = reader.read('p'); reader.reset();
  release(new Response(JSON.stringify(envelope(fixture())), {headers:{ETag:'"old"'}})); await old;
  const next = reader.read('p'); assert.deepEqual(headers, {});
  release(new Response(JSON.stringify(envelope(fixture())))); await next;
});

test('shared processing anchor keeps the source author and verifies delegation metadata', async () => {
 const page = envelope(fixture()), anchor = page.relay.checkpoint.anchor;
 const processing = {task_id:'task_one',lease:'claim',owner:'a.d',session_id:'s',version:1,sha256:'a'.repeat(64)};
 anchor.writer = 'helper.device'; anchor.commit.codec = 'coconet-json-v3'; anchor.commit.processing = processing;
 const commit = JSON.stringify({id:'commit',project_id:'p',actor:anchor.writer,base:1,processing});
 anchor.commit.payload = Buffer.from(commit).toString('base64'); anchor.commit.sha256 = createHash('sha256').update(commit).digest('hex');
 const graph = await publishedGraph(page, 'p'); assert.equal(graph.nodes[0].author.account_id,'a');
 anchor.commit.processing = {...processing,owner:'helper.device'};
 await assert.rejects(publishedGraph(page,'p'));
});

test('rewind checkpoint hides withdrawn nodes while retaining explicit Library evidence', async () => {
  const state = fixture();
  state.nodes.n.state = 'withdrawn'; state.nodes.n.relations = 'ready';
  state.snapshots['s/1'].superseded = true;
  state.library.n = 'explicit fixed evidence';
  const page = envelope(state);
  page.relay.checkpoint.checkpoint.codec = 'coconet-state-json-v2';
  page.relay.checkpoint.anchor.commit.codec = 'coconet-json-v4';
  const graph = await publishedGraph(page, 'p');
  assert.deepEqual(graph.nodes, []);
  assert.equal(graph.library.length, 1);
  assert.equal(graph.library[0].id, 'n');
  assert.equal(graph.pending, 1);
  assert.equal(graph.relations, 0);
  state.library = {};
  await assert.rejects(publishedGraph(envelope(state), 'p'));
  state.nodes.n.state = 'current';
  await assert.rejects(publishedGraph(envelope(state), 'p'));
});

test('editable rewind checkpoint projects restored and protected tail states', async () => {
  const state = fixture();
  state.sessions.s.tail = 'n';
  state.sessions.s.processed_through = state.sessions.s.latest;
  state.snapshots['s/2'] = { session_id: 's', version: 2, owner: 'a.d', rewrite: { previous_version: 1, common_turns: 1, restore_editable_tails: true } };
  state.nodes.n.relations = 'ready';
  for (const status of ['current', 'sealed']) {
    state.nodes.n.state = status;
    const page = envelope(state);
    page.relay.checkpoint.checkpoint.codec = 'coconet-state-json-v3';
    page.relay.checkpoint.anchor.commit.codec = 'coconet-json-v5';
    const graph = await publishedGraph(page, 'p');
    assert.equal(graph.nodes[0].state, status);
    assert.equal(graph.nodes[0].source.version, 1, 'restoration preserves the evidence source');
    assert.equal(publishedGraphStatus(graph).summary, 'published_graph_ready');
    page.relay.checkpoint.checkpoint.codec = 'coconet-state-json-future';
    await assert.rejects(publishedGraph(page, 'p'));
  }
});

test('Library displays its saved version independently of live progress and rewind', async () => {
  const state = fixture();
  state.library.n = 'saved by a colleague';
  state.library_sources = { n: { ...state.nodes.n, state: 'sealed', relations: 'ready', parents: [] } };
  state.nodes.n = { ...state.nodes.n, objective: 'Updated author progress', source: { session_id: 's', version: 2 } };
  state.sessions.s.processed_through = 2;
  state.snapshots['s/2'] = { session_id: 's', version: 2, owner: 'a.d' };
  const wrap = () => {
    const response = envelope(state);
    response.relay.checkpoint.checkpoint.codec = 'coconet-state-json-v4';
    response.relay.checkpoint.anchor.commit.codec = 'coconet-json-v6';
    return response;
  };
  let graph = await publishedGraph(wrap(), 'p');
  assert.equal(graph.nodes[0].source.version, 2);
  assert.equal(graph.nodes[0].library.source.version, 1);
  assert.equal(graph.library[0].source.version, 1);
  assert.notEqual(graph.library[0].objective, graph.nodes[0].objective);
  assert.match(publishedSessionActions('p', graph.library[0])[1].text, /"version": 1/);
  delete state.nodes.n;
  state.snapshots['s/1'].superseded = true;
  graph = await publishedGraph(wrap(), 'p');
  assert.equal(graph.nodes.length, 0);
  assert.equal(graph.library.length, 1);
  await assert.rejects(publishedGraph(envelope(state), 'p'), 'old codec must not declare new semantics');
  state.library_sources.n.source.version = 99;
  await assert.rejects(publishedGraph(wrap(), 'p'));
});
