import { basePath, endpointPath } from './endpoint.mjs';
const alphabet = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz';

// Match the server's canonical Base58 encoding of exactly 16 bytes, including
// leading zero bytes. A matching prefix/length alone can still exceed 128 bits.
export function isProjectID(id) {
  if (!/^coco_[1-9A-HJ-NP-Za-km-z]{16,22}$/.test(id)) return false;
  const encoded = id.slice(5);
  let value = 0n;
  for (const character of encoded) value = value * 58n + BigInt(alphabet.indexOf(character));
  const zeros = encoded.match(/^1*/)[0].length;
  const bytes = value === 0n ? 0 : Math.ceil(value.toString(2).length / 8);
  return zeros + bytes === 16;
}

export function projectFromPath(pathname, prefix = basePath()) {
  const start = prefix + '/';
  const id = pathname.startsWith(start) ? pathname.slice(start.length) : '';
  return isProjectID(id) ? id : '';
}

export function workspaceLocation(project, view = 'graph', selection = {}) {
  if (!project) return endpointPath('/');
  if (!isProjectID(project)) throw new Error('Invalid Project ID');
  const query = new URLSearchParams();
  if (['activity', 'library'].includes(view)) query.set('view', view);
  for (const key of ['node', 'event', 'entry', 'session', 'version']) {
    if (selection[key] !== undefined && selection[key] !== '') query.set(key, selection[key]);
  }
  return endpointPath('/' + project) + (query.size ? '#' + query : '');
}

export function parseWorkspaceLocation({ pathname, hash }) {
  const query = new URLSearchParams(hash.replace(/^#/, ''));
  const view = ['graph', 'activity', 'library'].includes(query.get('view')) ? query.get('view') : 'graph';
  return { project: projectFromPath(pathname), view, node: query.get('node') || '', event: query.get('event') || '', entry: query.get('entry') || '', session: query.get('session') || '', version: Number(query.get('version')) };
}
