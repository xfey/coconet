import { test } from 'node:test';
import assert from 'node:assert/strict';
import { basePath, endpointPath } from './assets/endpoint.mjs';
import { workspaceLocation, parseWorkspaceLocation } from './assets/catalog.mjs';
import { parseProjectInput, projectConnectionCommand } from './assets/connection.mjs';
import { createProjectEvents } from './assets/events.mjs';

test('subpath navigation, shared links, CLI commands and events retain deployment scope', t => {
  const prefix = '/teams/demo', origin = 'https://example.test', project = 'coco_1111111111111111';
  const previous = globalThis.document;
  globalThis.document = { querySelector: () => ({ content: prefix }) };
  t.after(() => { globalThis.document = previous; });
  assert.equal(basePath(), prefix);
  assert.equal(endpointPath('/v1/auth/start'), prefix + '/v1/auth/start');
  assert.equal(workspaceLocation(''), prefix + '/');
  const path = workspaceLocation(project, 'library', { node: 'work_1' });
  assert.equal(path, prefix + '/' + project + '#view=library&node=work_1');
  assert.equal(parseWorkspaceLocation(new URL(origin + path)).project, project);
  assert.equal(parseWorkspaceLocation(new URL(origin + '/other/' + project)).project, '');
  assert.equal(parseProjectInput(origin + path, [origin + prefix]), project);
  for (const bad of [origin + '/' + project, origin + '/teams/other/' + project, origin + prefix + '-other/' + project]) {
    assert.throws(() => parseProjectInput(bad, [origin + prefix]));
  }
  assert.ok(projectConnectionCommand(project, origin + prefix).endsWith("--server '" + origin + prefix + "'"));
  let streamURL;
  class Stream { constructor(url) { streamURL = url; } addEventListener() {} close() {} }
  const events = createProjectEvents({ refresh() {}, revoked() {}, EventSourceClass: Stream });
  t.after(events.stop);
  events.start(project, 1);
  assert.equal(streamURL, prefix + '/v1/dashboard/projects/' + project + '/events');
});
