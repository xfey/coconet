import assert from 'node:assert/strict';
import { test } from 'node:test';
import { isProjectID, workspaceLocation, parseWorkspaceLocation, projectFromPath } from './assets/catalog.mjs';

test('project paths require canonical 128-bit IDs and never consume auth fragments', () => {
  for (const id of ['coco_1111111111111111', 'coco_YcVfxkQb6JRzqk5kF2tNLv']) {
    assert.equal(isProjectID(id), true);
    assert.equal(projectFromPath('/' + id), id);
    assert.equal(workspaceLocation(id), '/' + id);
    assert.equal(parseWorkspaceLocation(new URL('https://example.test/' + id)).project, id);
  }
  for (const id of ['prj_1111111111111111', 'coco_111111111111111', 'coco_11111111111111111', 'coco_zzzzzzzzzzzzzzzzzzzzzz', 'coco_YcVfxkQb6JRzqk5kF2tNLw', 'coco_0OIl', ' coco_1111111111111111', 'coco_1111111111111111/extra']) {
    assert.equal(isProjectID(id), false, id);
    assert.equal(projectFromPath('/' + id), '');
  }
  assert.equal(parseWorkspaceLocation(new URL('https://example.test/#authorize=abc')).project, '');
  assert.equal(parseWorkspaceLocation(new URL('https://example.test/#project=coco_1111111111111111')).project, '');
  assert.equal(workspaceLocation(''), '/');
});

test('node deep links retain exact selection without a redundant graph parameter', () => {
  const project = 'coco_1111111111111111';
  const selection = { node: 'work_中文 1', session: 'sess_a', version: 3 };
  const path = workspaceLocation(project, 'graph', selection);
  assert.ok(path.startsWith('/' + project + '#node='));
  assert.ok(!path.includes('view='));
  assert.deepEqual(parseWorkspaceLocation(new URL(path, 'https://example.test')), { project, view: 'graph', ...selection, event: '', entry: '' });
  assert.equal(parseWorkspaceLocation(new URL(workspaceLocation(project, 'library'), 'https://example.test')).view, 'library');
});
