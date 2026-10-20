import assert from 'node:assert/strict';
import { test } from 'node:test';
import { loadProjectFolderExample } from './assets/installation.mjs';

test('directory guidance requires agreement across all pages, not matching absolute paths', async () => {
  const controller = new AbortController(), paths = [];
  const request = async (path, { signal }) => {
    assert.equal(signal, controller.signal); paths.push(path);
    return paths.length === 1
      ? { folders: [{ path: '/home/alice/mcp-zero/' }], next_cursor: 'next' }
      : { folders: [{ path: '/srv/team/mcp-zero' }] };
  };
  assert.equal(await loadProjectFolderExample('project', request, controller.signal), 'mcp-zero/');
  assert.deepEqual(paths, ['/projects/project/workspaces?limit=200', '/projects/project/workspaces?limit=200&cursor=next']);
  let calls = 0;
  assert.equal(await loadProjectFolderExample('project', async () => ++calls === 1
    ? { folders: [{ path: '/work/frontend' }], next_cursor: 'next' }
    : { folders: [{ path: '/work/backend' }] }), '');
});

test('missing or ambiguous directory information never invents a project folder', async () => {
  assert.equal(await loadProjectFolderExample('', () => assert.fail('no project to query')), '');
  for (const folders of [[], [{ path: '/' }], [{ path: '/work/repo' }, { path: '/work/repo-fork' }]]) {
    assert.equal(await loadProjectFolderExample('project', async () => ({ folders })), '');
  }
  await assert.rejects(loadProjectFolderExample('project', async () => { throw new Error('offline'); }), /offline/);
});
