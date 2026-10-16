// One stream per selected project. Reconnect sends the current durable revision;
// there is no periodic graph fetch. Failed reads retry only a pending update.
export function createProjectEvents({ refresh, revoked, connection = () => {}, EventSourceClass = globalThis.EventSource, delay = 200, retryDelay = 5000, heartbeatTimeout = 45000 }) {
  let stream, project = '', epoch = 0, applied = 0, pending = 0, running = false, timer, heartbeat, reconnect;
  function stop() {
    epoch++; stream?.close(); stream = null; clearTimeout(timer); timer = null;
    clearTimeout(heartbeat); clearTimeout(reconnect);
    project = ''; applied = pending = 0; running = false;
  }
  function schedule(wait = delay) {
    if (!timer && !running && pending > applied) timer = setTimeout(run, wait);
  }
  async function run() {
    timer = null;
    if (running || pending <= applied || !project) return;
    const generation = epoch, target = pending, selected = project;
    running = true;
    let failed = false;
    try {
      const result = await refresh(selected);
      failed = result === false;
      if (generation === epoch && !failed) applied = Math.max(applied, target);
    } catch { failed = true; }
    finally {
      if (generation === epoch) { running = false; schedule(failed ? retryDelay : delay); }
    }
  }
  function start(selected, revision = 0) {
    if (!selected || !EventSourceClass) { stop(); return; }
    if (selected === project && stream) { acknowledge(selected, revision); return; }
    stop(); project = selected; applied = revision;
    const generation = epoch;
    function connect() {
      const source = stream = new EventSourceClass(`/v1/dashboard/projects/${encodeURIComponent(project)}/events`);
      const current = () => generation === epoch && stream === source;
      function watch() {
        clearTimeout(heartbeat);
        heartbeat = setTimeout(() => {
          if (!current()) return;
          source.close(); stream = null; connection('connecting');
          reconnect = setTimeout(() => { if (generation === epoch) connect(); }, retryDelay);
        }, heartbeatTimeout);
      }
      function alive() { if (current()) { connection('online'); watch(); } }
      connection('connecting'); watch();
      source.addEventListener('open', alive);
      source.addEventListener('heartbeat', alive);
      source.addEventListener('error', () => { if (current()) connection('connecting'); });
      source.addEventListener('graph', event => {
        if (!current()) return;
        alive();
        let value;
        try { value = JSON.parse(event.data).revision; } catch { return; }
        if (!Number.isSafeInteger(value) || value < 1) return;
        pending = Math.max(pending, value); schedule();
      });
      source.addEventListener('access_revoked', () => {
        if (!current()) return;
        stop(); revoked();
      });
    }
    connect();
  }
  function acknowledge(selected, revision) {
    if (selected === project && Number.isSafeInteger(revision)) applied = Math.max(applied, revision);
  }
  return { start, stop, acknowledge };
}
