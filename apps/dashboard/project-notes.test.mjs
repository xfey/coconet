import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createProjectNotesState } from './assets/project-notes-state.mjs';

function fixture() {
  let counter = 0, view = { version: 0, notes: [] }, failure, gate;
  const requests = [], receipts = new Map();
  const clone = value => structuredClone(value);
  const error = code => Object.assign(new Error(code), { code });
  async function request(path, options) {
    if (!options) { const snapshot = clone(view); if (gate) { const hold = gate; gate = null; await hold; } return snapshot; }
    const body = JSON.parse(options.body), key = path.split('/').at(-1); requests.push({ key, ...options, parsed:body });
    if (!receipts.has(body.request_id)) {
      const index = view.notes.findIndex(note => note.id === key), note = view.notes[index];
      if (note && body.base_revision !== note.revision) throw error('note_conflict');
      if (!note && body.base_revision) throw error('note_missing');
      if (options.method === 'DELETE') view.notes.splice(index,1);
      else if (note) { note.text = body.text; note.revision++; }
      else view.notes.push({ id:key,text:body.text,revision:1,author:{account_id:'me'} });
      view.version++; receipts.set(body.request_id,true);
    }
    if (failure) { const current = failure; failure = null; throw current; }
    return clone(view);
  }
  const options = { project:'project',account:'me',request,id:()=> 'stable-id-'+(++counter) };
  return { state:createProjectNotesState(options),options,requests,view, fail:()=>{failure=error('network')}, holdRead:()=>new Promise(resolve=>{gate=new Promise(release=>resolve(release));}) };
}

test('blank drafts disappear; own notes autosave on end and foreign notes remain read-only', async () => {
  const f=fixture(),s=f.state;await s.refresh();
  const empty=s.add();s.edit(empty,'  ');await s.end(empty);assert.equal(f.requests.length,0);
  const id=s.add();s.edit(id,'Shared priorities');assert.equal(f.requests.length,0);await s.end(id);
  assert.equal(s.view.notes[0].text,'Shared priorities');assert.equal(s.drafts.size,0);
  f.view.notes.push({id:'foreign',text:'Other member',revision:1,author:{account_id:'other'}});f.view.version++;await s.refresh();
  assert.equal(s.begin('foreign'),false);await s.remove('foreign');assert.equal(f.requests.length,1);
});

test('lost acknowledgement retries the same intent before publishing later edits', async () => {
  const f=fixture(),s=f.state;await s.refresh();const id=s.add();s.edit(id,'First');f.fail();await s.end(id);
  assert.equal(s.drafts.get(id).error,'note_save_failed');s.begin(id);s.edit(id,'Second');await s.end(id);
  assert.deepEqual(f.requests[1],f.requests[0]);assert.equal(f.view.notes.length,1);assert.equal(f.view.notes[0].text,'Second');assert.equal(s.unsettled(),false);
});

test('a pending create must settle before deletion, without restoring a deleted note', async () => {
  const f=fixture(),s=f.state;await s.refresh();const id=s.add();s.edit(id,'Temporary');f.fail();await s.end(id);await s.remove(id);
  assert.deepEqual(f.requests.map(r=>r.method),['PUT','PUT','DELETE']);assert.equal(f.view.notes.length,0);assert.equal(s.drafts.size,0);
});

test('a late background read cannot roll back a successful save', async () => {
  const f=fixture(),s=f.state;await s.refresh();const release=await f.holdRead();const read=s.refresh();
  const id=s.add();s.edit(id,'New version');await s.end(id);release();await read;
  assert.equal(s.view.version,1);assert.equal(s.view.notes[0].text,'New version');
});

test('same-note conflict keeps the draft and requires explicit resolution', async () => {
  const f=fixture(),s=f.state;await s.refresh();const id=s.add();s.edit(id,'Original');await s.end(id);
  s.begin(id);s.edit(id,'Local change');f.view.notes[0].text='Other device';f.view.notes[0].revision++;f.view.version++;
  await s.end(id);assert.equal(s.drafts.get(id).text,'Local change');assert.equal(s.drafts.get(id).error,'note_conflict');
  const count=f.requests.length;await s.retry();assert.equal(f.requests.length,count);
  await s.resolve(id,true);assert.equal(f.view.notes[0].text,'Local change');assert.equal(s.drafts.size,0);
});

test('remote deletion does not resurrect a note; explicit republish uses a new ID', async () => {
  const f=fixture(),s=f.state;await s.refresh();const id=s.add();s.edit(id,'Original');await s.end(id);
  s.begin(id);s.edit(id,'Draft');f.view.notes=[];f.view.version++;await s.end(id);
  assert.equal(s.drafts.get(id).error,'note_missing');await s.resolve(id,true);
  assert.equal(f.view.notes.length,1);assert.notEqual(f.view.notes[0].id,id);assert.equal(f.view.notes[0].text,'Draft');
});

test('reload restores a lost-acknowledgement draft and retries its original request', async () => {
  const f=fixture(),s=f.state;await s.refresh();const id=s.add();s.edit(id,'Survives reload');f.fail();await s.end(id);
  const retained=structuredClone([...s.drafts.values()]);const restored=createProjectNotesState({...f.options,retained});
  await restored.refresh();await restored.retry();assert.deepEqual(f.requests[1],f.requests[0]);assert.equal(restored.unsettled(),false);
});

test('unacknowledged save replay after a remote deletion cannot restore old content', async () => {
  const f=fixture(),s=f.state;await s.refresh();const id=s.add();s.edit(id,'Published once');f.fail();await s.end(id);
  f.view.notes=[];f.view.version++;await s.retry();assert.equal(s.view.notes.length,0);assert.equal(s.drafts.get(id).error,'note_missing');
});

test('deletion requested during an in-flight save waits for its acknowledgement', async () => {
  const f=fixture();let release;
  const gate=new Promise(resolve=>{release=resolve;});let hold=true;
  const s=createProjectNotesState({...f.options,request:async(path,options)=>{
    const result=await f.options.request(path,options);if(options&&hold){hold=false;await gate;}return result;
  }});
  await s.refresh();const id=s.add();s.edit(id,'Brief note');const saving=s.end(id);
  await s.remove(id);assert.equal(f.requests.length,1);release();await saving;
  assert.deepEqual(f.requests.map(r=>r.method),['PUT','DELETE']);assert.equal(s.view.notes.length,0);assert.equal(s.unsettled(),false);
});

test('a replay cannot silently overwrite another device when local edits continued', async () => {
  const f=fixture(),s=f.state;await s.refresh();const id=s.add();s.edit(id,'Initial');f.fail();await s.end(id);
  s.begin(id);s.edit(id,'Continued local draft');f.view.notes[0].text='Changed elsewhere';f.view.notes[0].revision++;f.view.version++;
  await s.end(id);assert.equal(s.drafts.get(id).error,'note_conflict');assert.equal(f.view.notes[0].text,'Changed elsewhere');
});
