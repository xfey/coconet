import assert from 'node:assert/strict';
import { test } from 'node:test';
import { spawnSync } from 'node:child_process';
import { projectConnectionCommand } from './assets/connection.mjs';

test('connection instructions preserve exact project and private server arguments', () => {
  const project = "coco_' $(exit 9) `exit 9`";
  const origin = 'https://private.example:9443';
  const command = projectConnectionCommand(project, origin);
  const result = spawnSync('/bin/sh', ['-c', 'coconet() { printf "%s\\000" "$@"; }; ' + command], { encoding: 'utf8' });
  assert.equal(result.status, 0);
  assert.deepEqual(result.stdout.split('\0'), ['connect', project, '--server', origin, '']);
  assert.equal(projectConnectionCommand('', 'https://api.coconet.space'), 'coconet connect');
});

test('project input accepts IDs and same-deployment links without switching origins', async () => {
  const { parseProjectInput } = await import('./assets/connection.mjs');
  const origins = ['https://coconet.space', 'https://api.coconet.space'];
  const id = 'coco_1111111111111111';
  for (const input of [id, '  ' + id + ' ', origins[0] + '/' + id, origins[1] + '/' + id + '#node=work_1&session=sess_1&version=2']) assert.equal(parseProjectInput(input, origins), id);
  for (const input of ['', 'coco_0OIl', 'prj_1111111111111111', origins[0] + '/#authorize=abc', origins[0] + '/#project=' + id, 'https://elsewhere.test/' + id, origins[0] + '/wrong/' + id, 'https://user@coconet.space/' + id, origins[0] + '/' + id + '?redirect=other', origins[0] + '/' + id + '/extra', origins[0] + '/' + id + '/']) assert.throws(() => parseProjectInput(input, origins));
});
