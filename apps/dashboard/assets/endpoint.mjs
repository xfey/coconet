// The server supplies the public prefix even when a gateway strips it before
// forwarding. Keep requests and navigation inside this deployment.
export function basePath() {
  const value = globalThis.document?.querySelector?.('meta[name="coconet-base-path"]')?.content || '';
  return value.startsWith('/') ? value.replace(/\/$/, '') : '';
}

export function endpointPath(path) {
  return basePath() + path;
}
