import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createHash, webcrypto } from 'node:crypto';
import { requestID, sha256 } from './assets/platform.mjs';
import { projectConnectionCommand } from './assets/connection.mjs';

test('HTTP browser uses cryptographic IDs and verifies the same SHA-256 digests', async () => {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'crypto');
  Object.defineProperty(globalThis, 'crypto', { configurable: true, value: { getRandomValues: array => webcrypto.getRandomValues(array) } });
  try {
    const ids = new Set(Array.from({ length: 100 }, requestID));
    assert.equal(ids.size, 100);
    for (const id of ids) assert.match(id, /^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/);
    for (const text of ['', 'abc', '协作项目', 'a'.repeat(1000000)]) {
      const bytes = new TextEncoder().encode(text);
      assert.equal(await sha256(bytes), createHash('sha256').update(bytes).digest('hex'));
    }
  } finally { Object.defineProperty(globalThis, 'crypto', descriptor); }
});

test('HTTP connection command uses one explicit server argument', () => {
  assert.equal(projectConnectionCommand('coco_abc', 'http://dev.example.test:7788'), "coconet connect 'coco_abc' --server 'http://dev.example.test:7788'");
});
