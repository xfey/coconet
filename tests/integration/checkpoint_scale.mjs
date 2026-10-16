// Opt-in browser projection measurement. Input files are synthetic states from
// TestCheckpointScaleFixtures, never native Session history. No layout/model
// quality or production network throughput is claimed by this measurement.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFile, readdir } from 'node:fs/promises';
import { createServer } from 'node:http';
import { dirname, resolve, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const fixtures = process.env.COCONET_CHECKPOINT_SCALE_DIR;
if (!fixtures) throw new Error('Set COCONET_CHECKPOINT_SCALE_DIR to the exported synthetic fixtures');
const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const routes = new Map([['/', { type: 'text/html', body: '<!doctype html><title>Checkpoint scale fixture</title>' }]]);
for (const name of ['published-graph.mjs', 'graph.mjs']) routes.set('/'+name, { type: 'text/javascript', body: await readFile(join(root, 'apps/dashboard/assets', name)) });
const cases = [];
for (const name of (await readdir(fixtures)).filter(name => /^nodes_\d+_versions_\d+\.json$/.test(name)).sort()) {
  const payload = await readFile(join(fixtures, name)), state = JSON.parse(payload);
  assert.equal(state.project_id, 'scale_project');
  const commit = Buffer.from(JSON.stringify({ schema: 1, id: 'scale_anchor', project_id: state.project_id, actor: 'account.device', base: state.revision-1 }));
  const checkpoint = { schema_version: 1, revision: state.revision, commit_id: 'scale_anchor', codec: 'coconet-state-json-v1', payload: payload.toString('base64'), sha256: hash(payload) };
  const anchor = { schema_version: 1, project_id: state.project_id, revision: state.revision, writer: 'account.device', commit: { schema_version: 1, id: 'scale_anchor', base: state.revision-1, codec: 'coconet-json-v2', payload: commit.toString('base64'), sha256: hash(commit), state_sha256: hash(payload) } };
  const response = { schema_version: 1, event_revision: state.revision+1, authors: { account: 'Fixture author' }, relay: { schema_version: 1, generation: state.revision+1, head: state.revision, checkpoint: { schema_version: 1, project_id: state.project_id, writer: 'account.device', checkpoint, anchor } } };
  const body = JSON.stringify(response);
  routes.set('/'+name, { type: 'application/json', body });
  cases.push({ name, bytes: payload.length, response_bytes: Buffer.byteLength(body), nodes: Object.keys(state.nodes).length, snapshots: Object.keys(state.snapshots).length, oversized: payload.length > 16*1024*1024 });
}
assert.ok(cases.length);
const server = createServer((req, res) => {
  const route = routes.get(req.url);
  if (!route) { res.writeHead(404); res.end(); return; }
  res.writeHead(200, { 'Content-Type': route.type, 'Cache-Control': 'no-store' }); res.end(route.body);
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
let browser;
try {
  const { chromium } = await import(process.env.COCONET_PLAYWRIGHT_MODULE ? pathToFileURL(resolve(process.env.COCONET_PLAYWRIGHT_MODULE)).href : 'playwright');
  browser = await chromium.launch({ headless: true, executablePath: process.env.COCONET_BROWSER_EXECUTABLE });
  const url = `http://127.0.0.1:${server.address().port}`;
  const results = [];
  for (const fixture of cases) {
    const page = await browser.newPage();
    await page.goto(url);
    const measured = await page.evaluate(async fixture => {
      const { publishedGraph } = await import('/published-graph.mjs');
      const response = await (await fetch('/'+fixture.name)).json();
      const durations = [];
      let graph, error;
      for (let i = 0; i < 3; i++) {
        const start = performance.now();
        try { graph = await publishedGraph(response, 'scale_project'); } catch (e) { error = e.message; }
        durations.push(performance.now()-start);
      }
      return { durations_ms: durations, projected_nodes: graph?.nodes.length, error };
    }, fixture);
    if (fixture.oversized) { assert.equal(measured.error, 'published_graph_invalid'); assert.equal(measured.projected_nodes, undefined); }
    else { assert.equal(measured.error, undefined); assert.equal(measured.projected_nodes, fixture.nodes); }
    results.push({ ...fixture, ...measured });
    await page.close();
  }
  console.log(JSON.stringify({ browser: await browser.version(), measurement: 'checkpoint verification and projection only; excludes fetch, layout and rendering', results }, null, 2));
} finally {
  await browser?.close();
  await new Promise(resolve => server.close(resolve));
}
