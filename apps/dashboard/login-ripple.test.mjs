import assert from 'node:assert/strict';
import test from 'node:test';
import { createRippleSchedule } from './assets/login-ripple.mjs';

test('ripple births stay irregular within 1.5–4.5 seconds and average about three', () => {
  const groupsAt = createRippleSchedule(), births = new Map();
  for (let time = 0; time <= 10800; time += .5) {
    for (const group of groupsAt(time)) births.set(group.sequence, group.born);
  }
  const times = [...births.values()];
  const gaps = times.slice(1).map((time, index) => time - times[index]);
  assert.ok(gaps.length > 3500);
  assert.ok(gaps.every(gap => gap >= 1.5 && gap <= 4.5));
  assert.ok(new Set(gaps.map(gap => gap.toFixed(2))).size > 250);
  assert.ok(Math.abs(gaps.reduce((sum, gap) => sum + gap, 0) / gaps.length - 3) < .05);
});

test('living groups survive newer births and expire naturally with bounded storage', () => {
  const groupsAt = createRippleSchedule();
  const groups = groupsAt(18.7);
  assert.ok(groups.length > 3);
  assert.ok(groups.some(group => group.sequence === 0));
  assert.ok(!groupsAt(18.9).some(group => group.sequence === 0));
  for (let time = 19; time <= 10800; time += 1) {
    const current = groupsAt(time);
    assert.ok(current.length > 0 && current.length <= 13);
    assert.ok(current.every(group => group.born <= time && time - group.born <= 18.8));
  }
});

test('pause, rewind and reduced-motion composition keep the same schedule', () => {
  const groupsAt = createRippleSchedule();
  const opening = structuredClone(groupsAt(7.2 / 1.3));
  const later = structuredClone(groupsAt(75));
  assert.deepEqual(groupsAt(75), later);
  assert.deepEqual(groupsAt(7.2 / 1.3), opening);
  assert.deepEqual(groupsAt(75), later);
  assert.deepEqual(groupsAt(0), [{ sequence:0, born:0 }]);
  assert.deepEqual(groupsAt(75), later);
});
