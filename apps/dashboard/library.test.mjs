import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createLibraryActions } from './assets/library.mjs';

const node = { id: 'work', source: { session_id: 'session', version: 2 } };
const receipt = input => ({ ...input, state: input.remove ? 'removed' : 'collected', revision: 8 });
function deferred() { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; }

test('collection stays busy through confirmation and graph refresh, without duplicate requests', async () => {
  const reply = deferred(), refresh = deferred(), inputs = [];
  const actions = createLibraryActions({ request: async (_, input) => { inputs.push(input); await reply.promise; return receipt(input); }, accepted: () => refresh.promise });
  const operation = actions.change('project', node);
  assert.equal(actions.pending('project', node), true);
  assert.equal(await actions.change('project', node), false);
  reply.resolve(); await new Promise(resolve => setImmediate(resolve));
  assert.equal(actions.pending('project', node), true);
  refresh.resolve(); assert.equal(await operation, true);
  assert.equal(actions.pending('project', node), false); assert.equal(inputs.length, 1);
});

test('lost responses retry the exact selection; changed source or intent gets a new identity', async () => {
  const inputs = []; let fail = true;
  const actions = createLibraryActions({ request: async (_, input) => { inputs.push(input); if (fail) throw new Error('reply lost'); return receipt(input); } });
  await assert.rejects(actions.change('p', node)); fail = false;
  assert.equal(await actions.change('p', node), true);
  assert.deepEqual(inputs[0], inputs[1]);
  const saved = { ...node, library: { note: '' } };
  await actions.change('p', saved);
  assert.equal(inputs[2].remove, true); assert.notEqual(inputs[2].request_id, inputs[0].request_id);
  fail = true; await assert.rejects(actions.change('p', node));
  await assert.rejects(actions.change('p', { ...node, source: { ...node.source, version: 3 } }));
  assert.notEqual(inputs[3].request_id, inputs[4].request_id);
});

test('account/project reset ignores late replies and does not unlock a newer request', async () => {
  const old = deferred(), current = deferred(); let accepted = 0, changed = 0, calls = 0;
  const actions = createLibraryActions({ changed: () => changed++, accepted: () => accepted++, request: async (_, input) => { const gate = calls++ ? current : old; await gate.promise; return receipt(input); } });
  const first = actions.change('p', node); actions.reset(); const second = actions.change('p', node);
  const rendered = changed; old.resolve(); assert.equal(await first, false);
  assert.equal(changed, rendered); assert.equal(accepted, 0); assert.equal(actions.pending('p', node), true);
  current.resolve(); assert.equal(await second, true); assert.equal(accepted, 1);
});

test('mismatched acknowledgements never report collection success', async () => {
  let accepted = false;
  const actions = createLibraryActions({ accepted: () => accepted = true, request: async (_, input) => ({ ...receipt(input), source: { session_id: 'other', version: 2 } }) });
  await assert.rejects(actions.change('p', node)); assert.equal(accepted, false); assert.equal(actions.pending('p', node), false);
});

test('removing an older collection from the live graph keeps its saved source across retries', async () => {
  const inputs = []; let fail = true;
  const saved = { ...node, source: { session_id: 'session', version: 3 }, library: { note: '', source: node.source } };
  const actions = createLibraryActions({ request: async (_, input) => { inputs.push(input); if (fail) throw new Error('offline'); return receipt(input); } });
  await assert.rejects(actions.change('p', saved));
  fail = false;
  await actions.change('p', { ...saved, source: { ...saved.source, version: 4 } });
  assert.deepEqual(inputs[0], inputs[1]);
  assert.deepEqual(inputs[1].source, node.source);
});
