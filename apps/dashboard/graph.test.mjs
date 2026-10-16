import { test } from 'node:test';
import { readFileSync } from 'node:fs';
import { messages, detectLanguage, translate, errorKey } from './assets/i18n.mjs';
import assert from 'node:assert/strict';
import { createGraphLayout, graphStructure, edgePath, nodeWidth, nodeHeight } from './assets/graph.mjs';
import ELK from './assets/vendor/elk-0.12.0.api.js';
import ELKWorker from './assets/vendor/elk-0.12.0.worker.min.js';
import { createLayoutClient } from './assets/layout-client.mjs';
import { zoomAt, fitCanvas } from './assets/canvas.mjs';
import { publishedSessionActions } from './assets/published-graph.mjs';

const engine = new ELK({ algorithms: ['layered'], workerFactory: () => new ELKWorker.Worker() });
const layoutGraph = nodes => createGraphLayout(engine)(nodes);

function assertGeometry(nodes, layout) {
  assert.equal(layout.positions.size, nodes.length);
  const visible = nodes.map(node => ({ id: node.id, parents: layout.edges.filter(edge => edge.child === node.id).map(edge => edge.parent) }));
  assert.deepEqual(reachability(visible), reachability(nodes));
  for (const edge of layout.edges) assert.ok(nodes.find(node => node.id === edge.child).parents.includes(edge.parent));
  const positions = [...layout.positions.values()];
  for (let i = 0; i < positions.length; i++) {
    const p = positions[i];
    assert.ok(p.x >= 0 && p.y >= 0 && p.x + nodeWidth <= layout.width && p.y + nodeHeight <= layout.height);
    for (const q of positions.slice(i + 1)) assert.ok(Math.abs(p.x - q.x) >= nodeWidth || Math.abs(p.y - q.y) >= nodeHeight);
  }
  for (const edge of layout.edges) {
    const parent = layout.positions.get(edge.parent), child = layout.positions.get(edge.child);
    assert.ok(child.x > parent.x + nodeWidth);
    assert.ok(Math.abs(edge.points[0].x - parent.x - nodeWidth) < 1);
    assert.ok(Math.abs(edge.points.at(-1).x - child.x) < 1);
    for (let i = 1; i < edge.points.length; i++) {
      const a = edge.points[i-1], b = edge.points[i]; assert.ok(a.x === b.x || a.y === b.y);
      for (const [id,p] of layout.positions) {
        if (id === edge.parent || id === edge.child) continue;
        const crosses = a.x === b.x ? a.x > p.x && a.x < p.x+nodeWidth && Math.max(a.y,b.y) > p.y && Math.min(a.y,b.y) < p.y+nodeHeight : a.y > p.y && a.y < p.y+nodeHeight && Math.max(a.x,b.x) > p.x && Math.min(a.x,b.x) < p.x+nodeWidth;
        assert.equal(crosses,false);
      }
    }
  }
}

function reachability(nodes) {
  const parents = new Map(nodes.map(node => [node.id, node.parents]));
  return nodes.map(node => {
    const seen = new Set(), pending = [...node.parents];
    while (pending.length) {
      const id = pending.pop();
      if (seen.has(id)) continue;
      seen.add(id);
      pending.push(...parents.get(id));
    }
    return [node.id, [...seen].sort()];
  }).sort(([a], [b]) => a.localeCompare(b));
}

test('canvas hides transitive shortcuts while retaining branches, reachability and source parents', async () => {
  const nodes = [
    { id: 'a', parents: [] }, { id: 'b', parents: ['a'] }, { id: 'c', parents: ['b'] },
    { id: 'd', parents: ['a', 'b', 'c'] }, { id: 'e', parents: ['a', 'b', 'c', 'd'] },
    { id: 'f', parents: ['b', 'c'] }, { id: 'independent', parents: [] },
    { id: 'merge', parents: ['e', 'f', 'independent', 'a'] },
  ];
  const original = structuredClone(nodes), reduced = graphStructure(nodes);
  assert.deepEqual(reduced.find(node => node.id === 'e').parents, ['d']);
  assert.deepEqual(reduced.find(node => node.id === 'f').parents, ['c']);
  assert.deepEqual(reduced.find(node => node.id === 'merge').parents, ['e', 'f', 'independent']);
  assert.deepEqual(reachability(reduced), reachability(original));
  assert.deepEqual(graphStructure(reduced), reduced);
  assert.deepEqual(graphStructure(nodes.toReversed().map(node => ({ ...node, parents: node.parents.toReversed() }))), reduced);
  const layout = await layoutGraph(nodes);
  assert.equal(layout.edges.length, 8);
  assertGeometry(nodes, layout);
  assert.deepEqual(nodes, original);
});

test('dense ancestral shortcuts reduce to a chain without recursive traversal', () => {
  const nodes = Array.from({ length: 300 }, (_, i) => ({ id: String(i), parents: Array.from({ length: i }, (_, p) => String(p)) }));
  const reduced = graphStructure(nodes);
  assert.equal(reduced.reduce((count, node) => count + node.parents.length, 0), nodes.length - 1);
  for (const node of reduced) assert.deepEqual(node.parents, node.id === '0' ? [] : [String(Number(node.id) - 1)]);
});

test('every DAG on five topologically ordered nodes keeps exactly its essential reachability edges', () => {
  const possible = [];
  for (let child = 1; child < 5; child++) for (let parent = 0; parent < child; parent++) possible.push([parent, child]);
  for (let mask = 0; mask < 2 ** possible.length; mask++) {
    const nodes = Array.from({ length: 5 }, (_, i) => ({ id: String(i), parents: [] }));
    for (let bit = 0; bit < possible.length; bit++) if (mask & 2 ** bit) {
      const [parent, child] = possible[bit]; nodes[child].parents.push(String(parent));
    }
    const reduced = graphStructure(nodes), expected = reachability(nodes);
    assert.deepEqual(reachability(reduced), expected);
    for (const node of reduced) for (const parent of node.parents) {
      const without = reduced.map(other => other.id === node.id ? { ...other, parents: other.parents.filter(id => id !== parent) } : other);
      assert.notDeepEqual(reachability(without), expected);
    }
  }
});

test('all roots, branches and merge dependencies retain a nonoverlapping forward layout', async () => {
  const nodes = [{id:'merge',parents:['left','right']},{id:'isolated',parents:[]},{id:'right',parents:['root']},{id:'root',parents:[]},{id:'left',parents:['root']}];
  const layout = await layoutGraph(nodes);
  assert.equal(layout.order.length, nodes.length);
  for (const node of nodes) for (const parent of node.parents) assert.ok(layout.positions.get(parent).x + nodeWidth < layout.positions.get(node.id).x);
  for (const [index, a] of nodes.entries()) for (const b of nodes.slice(index + 1)) {
    const p = layout.positions.get(a.id), q = layout.positions.get(b.id);
    assert.ok(Math.abs(p.x-q.x) >= nodeWidth || Math.abs(p.y-q.y) >= nodeHeight);
  }
  assertGeometry(nodes,layout);
  assert.deepEqual(await layoutGraph(nodes.toReversed()), layout);
});

test('invalid graph references and cycles fail before a partial graph is drawn', () => {
  assert.throws(() => graphStructure([{id:'a',parents:['missing']}]));
  assert.throws(() => graphStructure([{id:'a',parents:['b']},{id:'b',parents:['a']}]));
  assert.throws(() => graphStructure([{id:'a',parents:[]},{id:'a',parents:[]}]));
});

test('forks and merges center on their branches, chains align and independent work stays separate', async () => {
  const nodes = [{id:'a',parents:[]},{id:'b',parents:['a']},{id:'c',parents:['a']},{id:'d',parents:['b','c']},{id:'e',parents:['d']},{id:'z',parents:[]}];
  const {positions:p} = await layoutGraph(nodes);
  const center = (p.get('b').y + p.get('c').y) / 2;
  assert.ok(Math.abs(p.get('a').y - center) < 1);
  assert.ok(Math.abs(p.get('d').y - center) < 1);
  assert.ok(Math.abs(p.get('e').y - center) < 1);
  assert.ok(p.get('z').y > p.get('c').y + nodeHeight);
});

test('uneven multiple-root graphs keep all nodes separate with deterministic layout', async () => {
  const nodes = Array.from({length:48}, (_,i) => ({id:'node_'+String(i).padStart(2,'0'),parents:i<5?[]:[...new Set([Math.floor(i/2),i%7===0?i-1:Math.floor(i/3)])].map(p=>'node_'+String(p).padStart(2,'0'))}));
  const layout = await layoutGraph(nodes);
  for (const node of nodes) for (const parent of node.parents) assert.ok(layout.positions.get(node.id).x > layout.positions.get(parent).x);
  const points = [...layout.positions.values()];
  for (let i=0;i<points.length;i++) for (const b of points.slice(i+1)) assert.ok(Math.abs(points[i].x-b.x)>=nodeWidth || Math.abs(points[i].y-b.y)>=nodeHeight+47);
  assertGeometry(nodes,layout);
  assert.deepEqual(await layoutGraph(nodes.toReversed()),layout);
});

test('camera zoom keeps its world anchor fixed and fit bounds remains readable', () => {
  const camera = { x: -70, y: 45, zoom: .7 }, anchor = { x: 350, y: 280 };
  for (const target of [.01, .5, 1.2, 100]) {
    const next = zoomAt(camera, target, anchor);
    assert.ok(next.zoom >= .25 && next.zoom <= 2);
    assert.ok(Math.abs((anchor.x - next.x) / next.zoom - (anchor.x - camera.x) / camera.zoom) < 1e-9);
    assert.ok(Math.abs((anchor.y - next.y) / next.zoom - (anchor.y - camera.y) / camera.zoom) < 1e-9);
  }
  const fit = fitCanvas({ width: 900, height: 600 }, { width: 1200, height: 800 });
  assert.ok(fit.x >= 32 && fit.y >= 80 && fit.zoom <= 1);
  assert.ok(fit.y + 600 * fit.zoom <= 800 - 96);
});

test('short branches stay near their merge and incoming routes use separate ports', async () => {
  const nodes = [{id:'a',parents:[]},{id:'b',parents:[]},{id:'c',parents:['b']},{id:'d',parents:['a','c']}];
  const layout = await layoutGraph(nodes); assertGeometry(nodes,layout);
  assert.equal(layout.positions.get('a').x,layout.positions.get('c').x);
  const incoming = layout.edges.filter(edge=>edge.child==='d');
  assert.notEqual(incoming[0].points.at(-1).y,incoming[1].points.at(-1).y);
  assert.ok(incoming.some(edge=>edgePath(edge.points).includes('Q')));
  assert.ok(layout.edges.every(edge=>!/[CHV]/.test(edgePath(edge.points))));
});

test('cached components survive metadata changes and unrelated additions; only changed groups invoke ELK', async () => {
  let calls=0;
  const layout=createGraphLayout({layout: input=>{calls++;return engine.layout(input);}});
  const nodes=[{id:'b',parents:[]},{id:'c',parents:['b']},{id:'branch',parents:['b']}];
  const first=await layout(nodes); assert.equal(calls,1);
  const renamed=await layout(nodes.toReversed().map(node=>({...node,objective:'New title',source:{version:2}})));
  assert.deepEqual(renamed,first); assert.equal(calls,1);
  const added=[...nodes,{id:'a',parents:[]},{id:'d',parents:['a']},{id:'other',parents:['a']},{id:'single',parents:[]}];
  const second=await layout(added); assert.equal(calls,2); assertGeometry(added,second);
  for(const node of nodes)assert.deepEqual(second.positions.get(node.id),first.positions.get(node.id));
  const grown=[...added,{id:'tail',parents:['c']}];
  const third=await layout(grown); assert.equal(calls,3); assertGeometry(grown,third);
  assert.deepEqual(third.positions.get('a').x,second.positions.get('a').x);
  assert.deepEqual(third.positions.get('d').y-third.positions.get('a').y,second.positions.get('d').y-second.positions.get('a').y);
});

test('empty and disconnected graphs stay compact without invoking the engine', async () => {
  const layout=createGraphLayout({layout:()=>assert.fail('ELK unnecessary for isolated nodes')});
  assert.equal((await layout([])).positions.size,0);
  const nodes=Array.from({length:20},(_,i)=>({id:String(i),parents:[]}));
  const result=await layout(nodes); assertGeometry(nodes,result); assert.equal(new Set([...result.positions.values()].map(p=>p.x)).size,3);
});

test('long chains retain every source edge without recursive engine traversal', async () => {
  const layout = createGraphLayout({ layout: () => assert.fail('a chain does not need ELK') });
  const nodes = Array.from({ length: 5000 }, (_, i) => ({ id: String(i), parents: i ? [String(i - 1)] : [] }));
  const result = await layout(nodes.toReversed());
  assert.equal(result.positions.size, nodes.length);
  assert.equal(result.edges.length, nodes.length - 1);
  for (let i = 0; i < nodes.length; i++) {
    const position = result.positions.get(String(i));
    assert.ok(position.x >= 0 && position.x + nodeWidth <= result.width);
    if (!i) continue;
    const edge = result.edges[i - 1], before = result.positions.get(String(i - 1));
    assert.equal(edge.parent, String(i - 1)); assert.equal(edge.child, String(i));
    assert.equal(position.y, before.y); assert.ok(position.x > before.x + nodeWidth);
    assert.deepEqual(edge.points, [{ x: before.x + nodeWidth, y: before.y + nodeHeight / 2 }, { x: position.x, y: position.y + nodeHeight / 2 }]);
  }
  assert.deepEqual(await layout(nodes), result);
  assertGeometry(nodes.slice(0, 8), await layout(nodes.slice(0, 8)));
});

test('malformed engine results cannot publish a partial graph or poison the component cache', async () => {
  let fail=false,calls=0;
  const layout=createGraphLayout({layout:input=>{calls++;return fail?{width:100,height:100,children:[],edges:[]}:engine.layout(input);}});
  const nodes=[{id:'a',parents:[]},{id:'b',parents:['a']},{id:'branch',parents:['a']}],first=await layout(nodes);fail=true;
  await assert.rejects(layout([...nodes,{id:'c',parents:['b']}]),/graph_layout_failed/);
  assert.deepEqual(await layout(nodes),first);assert.equal(calls,2);
  assert.throws(()=>graphStructure([{id:'a',parents:['a']}]),/graph_cycle/);
  assert.throws(()=>graphStructure([{id:'a',parents:['x','x']}]),/graph_parents_invalid/);
});

function controlledClient(timeoutMs = 1000) {
  const workers = [];
  const client = createLayoutClient({ timeoutMs,
    createWorker: () => { const worker = { terminated: false, terminate() { this.terminated = true; } }; workers.push(worker); return worker; },
    createEngine: worker => ({ layout: input => new Promise((resolve, reject) => { Object.assign(worker, { input, resolve, reject }); }) }),
  });
  return { workers, client };
}
const pair = [{ id: 'a', parents: [], objective: 'private text' }, { id: 'b', parents: ['a'], author: 'private account' }, { id: 'c', parents: ['a'] }];
test('worker client caches topology and sends no source text or account data', async () => {
  const { workers, client } = controlledClient();
  const pending = client.layout(pair);
  assert.doesNotMatch(JSON.stringify(workers[0].input), /private|objective|author/);
  workers[0].resolve(await engine.layout(workers[0].input));
  const result = await pending;
  assert.equal(await client.layout(pair.toReversed().map(n => ({ ...n, objective: 'changed' }))), result);
  assert.equal(workers.length, 1); client.reset();
});
test('worker cancellation rejects obsolete work and ignores late results after project reset', async () => {
  const { workers, client } = controlledClient();
  const first = client.layout(pair), rejected = assert.rejects(first, { name: 'AbortError' }); client.reset(); await rejected;
  const second = client.layout([{ id: 'x', parents: [] }, { id: 'y', parents: ['x'] }, { id: 'z', parents: ['x'] }]);
  assert.equal(workers[0].terminated, true);
  workers[0].resolve(await engine.layout(workers[0].input));
  workers[1].resolve(await engine.layout(workers[1].input));
  assert.deepEqual((await second).order, ['x', 'y', 'z']); client.reset();
});
test('worker failures and timeouts can retry while empty and isolated graphs need no worker', async () => {
  const { workers, client } = controlledClient(100);
  assert.equal((await client.layout([])).positions.size, 0);
  await client.layout([{ id: 'solo', parents: [] }]); assert.equal(workers.length, 0);
  const failed = client.layout(pair); workers[0].onerror(); await assert.rejects(failed, /graph_layout_failed/);
  await assert.rejects(client.layout(pair), /graph_layout_failed/); assert.equal(workers[1].terminated, true);
  const retry = client.layout(pair); workers[2].resolve(await engine.layout(workers[2].input)); assertGeometry(pair, await retry); client.reset();
});
test('new topology cancels active layout and a return to cached topology needs no worker', async () => {
  const { workers, client } = controlledClient();
  const first = client.layout(pair); workers[0].resolve(await engine.layout(workers[0].input)); const original = await first;
  const pending = client.layout([...pair, { id: 'tail', parents: ['b'] }]);
  const rejected = assert.rejects(pending, { name: 'AbortError' });
  assert.equal(await client.layout(pair), original); await rejected;
  assert.equal(workers[0].terminated, true); client.reset();
});

test('language preference overrides the browser and unsupported locales fall back predictably', () => {
  assert.equal(detectLanguage('en',['zh-CN']),'en');
  assert.equal(detectLanguage('zh',['en-US']),'zh');
  assert.equal(detectLanguage(null,['zh-TW','en-US']),'zh');
  assert.equal(detectLanguage('invalid',['fr-FR','en-GB','zh']),'en');
  assert.equal(detectLanguage(null,['fr-FR']),'en');
});

test('every interface message and declarative label has both languages and matching placeholders', () => {
  for (const [key,pair] of Object.entries(messages)) {
    assert.equal(pair.length,2,key); assert.ok(pair.every(text=>typeof text==='string' && text.length),key);
    assert.deepEqual(pair[0].match(/\{\w+\}/g),pair[1].match(/\{\w+\}/g),key);
  }
  const html=readFileSync(new URL('./assets/index.html',import.meta.url),'utf8');
  for (const [,key] of html.matchAll(/data-i18n(?:-aria-label|-title|-placeholder)?="([^"]+)"/g)) assert.ok(messages[key],key);
  assert.equal(translate('zh','library'),'收藏会话'); assert.equal(translate('en','library'),'Saved conversations');
  assert.equal(translate('en','fit_zoom',{percent:80}),'Fit canvas · 80%');
});

test('fixed Agent instructions stay English while their interface labels and errors are localized', () => {
  for (const action of publishedSessionActions('coco_fixed',{id: 'work_fixed', agent: 'codex', source: {session_id:'sess_fixed',version:2}})) {
    assert.ok(translate('zh',action.labelKey)); assert.ok(translate('en',action.descriptionKey));
    assert.doesNotMatch(action.text,/[\u4e00-\u9fff]/); assert.match(action.text,/"version": 2/);
  }
  assert.equal(errorKey({code:'membership_required'}),'membership_required');
  assert.equal(errorKey(new Error('dialogue_discontinuous')),'dialogue_discontinuous');
  assert.equal(errorKey(new TypeError('network details')),'error_generic');
});
