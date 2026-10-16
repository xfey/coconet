import './vendor/elk-0.12.0.api.js';
import { graphStructure, createGraphLayout } from './graph.mjs';

// The official ELK API delegates layout to one dedicated classic Worker. Only
// topology and dimensions reach it; component caching and rendering stay here.
export function createLayoutClient({
  createWorker = () => new Worker(new URL('./vendor/elk-0.12.0.worker.min.js', import.meta.url)),
  createEngine = worker => new globalThis.ELK({ algorithms: ['layered'], workerFactory: () => worker }),
  timeoutMs = 15000,
} = {}) {
  let worker, engine, calculate, active, cachedKey, cachedLayout, generation = 0;
  function stop(error = new Error('graph_layout_failed')) {
    generation++; worker?.terminate(); worker = engine = calculate = null;
    if (active) { clearTimeout(active.timer); const pending = active; active = null; pending.reject(error); }
  }
  function layoutEngine() {
    if (engine) return engine;
    const current = createWorker(); worker = current;
    current.onerror = current.onmessageerror = () => { if (current === worker) stop(); };
    engine = createEngine(current);
    return engine;
  }
  return {
    layout(nodes) {
      let structure;
      try { structure = graphStructure(nodes); } catch (error) { return Promise.reject(error); }
      const key = JSON.stringify(structure);
      if (active?.key === key) return active.promise;
      if (active) stop(new DOMException('Layout superseded', 'AbortError'));
      if (key === cachedKey) return Promise.resolve(cachedLayout);
      let resolve, reject;
      const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
      const job = { key, promise, resolve, reject, timer: setTimeout(() => stop(), timeoutMs) };
      active = job;
      if (!calculate) {
        const current = generation;
        calculate = createGraphLayout({ layout: input => {
          if (current !== generation) throw new DOMException('Layout superseded', 'AbortError');
          return layoutEngine().layout(input);
        } });
      }
      calculate(structure).then(layout => {
        if (active !== job) return;
        clearTimeout(job.timer); active = null;
        cachedKey = key; cachedLayout = layout; resolve(layout);
      }, () => { if (active === job) stop(); });
      return promise;
    },
    reset() { stop(new DOMException('Project changed', 'AbortError')); cachedKey = cachedLayout = undefined; },
  };
}
