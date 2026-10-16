import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createProjectEvents } from './assets/events.mjs';

class Stream {
  static instances = [];
  constructor(url) { this.url = url; this.handlers = {}; Stream.instances.push(this); }
  addEventListener(name, handler) { this.handlers[name] = handler; }
  close() { this.closed = true; }
  emit(revision) { this.handlers.graph({ data: JSON.stringify({ revision }) }); }
}
const tick = () => new Promise(resolve => setTimeout(resolve, 15));

test('heartbeats keep status alive without fetching graphs; silent streams reconnect', t => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  let reads = 0; const states = [];
  const events = createProjectEvents({ refresh: () => reads++, revoked() {}, connection: state => states.push(state), EventSourceClass: Stream, retryDelay: 100 });
  t.after(events.stop);
  events.start('one', 2); const first = Stream.instances.at(-1);
  first.handlers.open(); t.mock.timers.tick(20000); first.handlers.heartbeat();
  t.mock.timers.tick(40000); assert.equal(first.closed, undefined); assert.equal(reads, 0);
  t.mock.timers.tick(5000); assert.equal(first.closed, true); assert.equal(states.at(-1), 'connecting');
  t.mock.timers.tick(100); const next = Stream.instances.at(-1); assert.notEqual(first, next);
  first.handlers.open(); assert.equal(states.at(-1), 'connecting');
  next.handlers.open(); assert.equal(states.at(-1), 'online');
  events.stop(); const count = states.length;
  next.handlers.error(); t.mock.timers.tick(100000); assert.equal(states.length, count);
});

test('failed graph reads are retried without acknowledging the revision', async () => {
  let count = 0;
  const events = createProjectEvents({ refresh: async () => ++count > 1, revoked() {}, EventSourceClass: Stream, delay: 1, retryDelay: 1 });
  events.start('one', 1); const stream = Stream.instances.at(-1);
  stream.emit(2); await tick(); await tick();
  assert.equal(count, 2);
  stream.emit(2); await tick(); assert.equal(count, 2);
  events.stop();
});

test('stopping and reopening repairs a revision gap without background polling', async () => {
  let count = 0;
  const events = createProjectEvents({ refresh: async () => count++, revoked() {}, EventSourceClass: Stream, delay: 1 });
  events.start('one', 2); const old = Stream.instances.at(-1);
  events.stop(); old.emit(3); await tick(); assert.equal(count, 0);
  events.start('one', 2); Stream.instances.at(-1).emit(6); await tick();
  assert.equal(count, 1); events.stop();
});

test('events coalesce revisions, recover reconnect gaps, and ignore old projects', async () => {
  const reads = [];
  const events = createProjectEvents({ refresh: async project => reads.push(project), revoked() {}, EventSourceClass: Stream, delay: 1 });
  events.start('one', 3); const first = Stream.instances.at(-1);
  first.emit(3); await tick(); assert.equal(reads.length, 0);
  first.emit(4); first.emit(5); await tick(); assert.deepEqual(reads, ['one']);
  first.emit(5); await tick(); assert.equal(reads.length, 1);
  events.start('two', 1); const second = Stream.instances.at(-1);
  first.emit(100); second.emit(8); await tick(); assert.deepEqual(reads, ['one', 'two']);
  assert.equal(first.closed, true); events.stop();
});

test('revision arriving during refresh is consumed afterwards; revocation stops stream', async () => {
  let release, count = 0, revoked = 0;
  const events = createProjectEvents({ refresh: async () => { count++; if (count === 1) await new Promise(resolve => release = resolve); }, revoked: () => revoked++, EventSourceClass: Stream, delay: 1 });
  events.start('one', 1); const stream = Stream.instances.at(-1);
  stream.emit(2); await tick(); stream.emit(3); release(); await tick();
  assert.equal(count, 2);
  stream.handlers.access_revoked(); assert.equal(revoked, 1); assert.equal(stream.closed, true);
  stream.emit(4); await tick(); assert.equal(count, 2);
});
