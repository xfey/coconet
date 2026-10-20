import { requestID } from './platform.mjs';
// A retry keeps its exact intent. UI updates always come from the published
// graph; a receipt for an older edit must never overwrite a newer collection.
export function createLibraryActions({ request, accepted = async () => {}, changed = () => {} }) {
  let epoch = 0;
  const attempts = new Map();
  const key = (project, node) => `${project}/${node.id}`;
  return {
    get epoch() { return epoch; },
    reset() { epoch++; attempts.clear(); },
    pending(project, node) { return !!attempts.get(key(project, node))?.busy; },
    async change(project, node) {
      const id = key(project, node), remove = !!node.library, currentEpoch = epoch;
      let attempt = attempts.get(id);
      if (attempt?.busy) return false;
      if (!attempt || attempt.input.remove !== remove || attempt.input.source.session_id !== node.source.session_id || attempt.input.source.version !== node.source.version) {
        attempt = { input: { schema_version: 1, request_id: requestID(), work_node_id: node.id, source: { ...node.source }, remove }, busy: false };
        attempts.set(id, attempt);
      }
      attempt.busy = true; changed();
      try {
        const result = await request(project, attempt.input);
        if (epoch !== currentEpoch) return false;
        if (result?.schema_version !== 1 || result.request_id !== attempt.input.request_id || result.work_node_id !== node.id || result.source?.session_id !== node.source.session_id || result.source.version !== node.source.version || result.state !== (remove ? 'removed' : 'collected') || !Number.isSafeInteger(result.revision) || result.revision < 1) throw new Error('library_update_failed');
        await accepted(project, result);
        if (epoch !== currentEpoch) return false;
        attempts.delete(id);
        return true;
      } finally {
        attempt.busy = false;
        if (epoch === currentEpoch) {
          // Bound retry metadata without evicting any in-flight operation.
          for (const [savedID, saved] of attempts) if (attempts.size > 64 && !saved.busy && savedID !== id) attempts.delete(savedID);
          changed();
        }
      }
    },
  };
}
