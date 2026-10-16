export const nodeWidth = 240, nodeHeight = 128;
const compare = (a, b) => a < b ? -1 : a > b ? 1 : 0;
const gap = 48, componentGap = 64;
export const emptyLayout = () => ({ width: 320, height: 256, positions: new Map(), edges: [], order: [] });

// The canvas receives topology only, never session content or account details.
// Hide transitive edges for layout; the source graph retains every relationship.
export function graphStructure(nodes) {
  if (!Array.isArray(nodes) || nodes.some(node => typeof node?.id !== 'string' || !node.id)) throw new Error('graph_invalid');
  const byID = new Map(nodes.map(node => [node.id, node]));
  if (byID.size !== nodes.length) throw new Error('graph_duplicate');
  const indegree = new Map(), children = new Map();
  for (const node of nodes) {
    if (!Array.isArray(node.parents) || node.parents.some(id => typeof id !== 'string') || new Set(node.parents).size !== node.parents.length) throw new Error('graph_parents_invalid');
    indegree.set(node.id, node.parents.length); children.set(node.id, []);
  }
  for (const node of nodes) for (const parent of node.parents) {
    if (!byID.has(parent)) throw new Error('graph_parent_missing');
    children.get(parent).push(node.id);
  }
  const ready = nodes.filter(node => !node.parents.length).map(node => node.id);
  for (let i = 0; i < ready.length; i++) for (const child of children.get(ready[i])) {
    indegree.set(child, indegree.get(child) - 1);
    if (!indegree.get(child)) ready.push(child);
  }
  if (ready.length !== nodes.length) throw new Error('graph_cycle');
  const rank = new Map(ready.map((id, i) => [id, i])), reduced = new Map();
  for (const id of ready) {
    const parents = [...byID.get(id).parents].sort((a, b) => rank.get(b) - rank.get(a));
    const kept = [], covered = new Set(), earliest = rank.get(parents.at(-1));
    for (const parent of parents) {
      if (covered.has(parent)) continue;
      kept.push(parent);
      if (parents.length < 2) continue;
      const pending = [parent];
      while (pending.length) {
        const ancestor = pending.pop();
        if (covered.has(ancestor) || rank.get(ancestor) < earliest) continue;
        covered.add(ancestor);
        // Already reduced predecessors avoid repeatedly walking dense shortcuts.
        for (const next of reduced.get(ancestor)) pending.push(next);
      }
    }
    reduced.set(id, kept);
  }
  return nodes.map(node => ({ id: node.id, parents: reduced.get(node.id).sort(compare) })).sort((a, b) => compare(a.id, b.id));
}

function components(nodes) {
  const adjacent = new Map(nodes.map(node => [node.id, []])), byID = new Map(nodes.map(node => [node.id, node]));
  for (const node of nodes) for (const parent of node.parents) { adjacent.get(node.id).push(parent); adjacent.get(parent).push(node.id); }
  const seen = new Set(), groups = [];
  for (const node of nodes) {
    if (seen.has(node.id)) continue;
    const ids = [node.id]; seen.add(node.id);
    for (let i = 0; i < ids.length; i++) for (const next of adjacent.get(ids[i])) if (!seen.has(next)) { seen.add(next); ids.push(next); }
    groups.push(ids.sort(compare).map(id => byID.get(id)));
  }
  return groups;
}

export const layoutOptions = {
  'elk.algorithm': 'layered', 'elk.direction': 'RIGHT', 'elk.edgeRouting': 'ORTHOGONAL',
  'elk.spacing.nodeNode': String(gap), 'elk.layered.spacing.nodeNodeBetweenLayers': '96',
  'elk.padding': '[top=32,left=24,bottom=32,right=24]', 'elk.randomSeed': '1',
  'elk.layered.nodePlacement.bk.fixedAlignment': 'BALANCED',
  'elk.layered.spacing.edgeNodeBetweenLayers': '24',
  'elk.layered.mergeEdges': 'false', 'elk.layered.considerModelOrder.strategy': 'NODES_AND_EDGES',
};

async function layoutComponent(nodes, engine) {
  // A single chain has an exact layout. Iterating it avoids ELK's recursive
  // traversal overflowing on long histories, without dropping nodes or edges.
  const next = new Map();
  let root;
  const chain = nodes.every(node => {
    if (!node.parents.length) { root = node.id; return true; }
    if (node.parents.length !== 1 || next.has(node.parents[0])) return false;
    next.set(node.parents[0], node.id); return true;
  });
  if (chain) {
    const positions = new Map(), edges = [];
    const spacing = Number(layoutOptions['elk.layered.spacing.nodeNodeBetweenLayers']);
    for (let id = root, i = 0; id !== undefined; id = next.get(id), i++) {
      const x = 24 + i * (nodeWidth + spacing), y = 32;
      positions.set(id, { x, y });
      const child = next.get(id);
      if (child !== undefined) edges.push({ parent: id, child, points: [{ x: x + nodeWidth, y: y + nodeHeight / 2 }, { x: x + nodeWidth + spacing, y: y + nodeHeight / 2 }] });
    }
    return { width: 48 + nodes.length * nodeWidth + (nodes.length - 1) * spacing, height: 64 + nodeHeight, positions, edges };
  }
  // Internal IDs keep arbitrary source IDs out of ELK's identifier namespace.
  const ids = new Map(nodes.map((node, i) => [node.id, 'n' + i]));
  const edges = nodes.flatMap(node => node.parents.map(parent => ({ parent, child: node.id })));
  const input = { id: 'root', layoutOptions, children: nodes.map(node => ({ id: ids.get(node.id), width: nodeWidth, height: nodeHeight })),
    edges: edges.map((edge, i) => ({ id: 'e' + i, sources: [ids.get(edge.parent)], targets: [ids.get(edge.child)] })) };
  const result = await engine.layout(input);
  const finite = value => Number.isFinite(value) && value >= 0;
  const validPoint = point => point && finite(point.x) && finite(point.y) && point.x <= result.width && point.y <= result.height;
  if (!finite(result.width) || !finite(result.height) || result.children?.length !== nodes.length || result.edges?.length !== edges.length) throw new Error('graph_layout_failed');
  const byID = new Map(result.children.map(node => [node.id, node])), byEdge = new Map(result.edges.map(edge => [edge.id, edge]));
  const positions = new Map(nodes.map(node => {
    const value = byID.get(ids.get(node.id));
    if (!validPoint(value) || value.x + nodeWidth > result.width || value.y + nodeHeight > result.height) throw new Error('graph_layout_failed');
    return [node.id, { x: value.x, y: value.y }];
  }));
  const routed = edges.map((edge, i) => {
    const sections = byEdge.get('e' + i)?.sections;
    if (sections?.length !== 1) throw new Error('graph_layout_failed');
    const section = sections[0], points = [section.startPoint, ...(section.bendPoints || []), section.endPoint];
    if (!points.every(validPoint)) throw new Error('graph_layout_failed');
    // ELK can return sub-pixel floating-point noise on an orthogonal segment.
    // Snap only within numerical tolerance; genuine diagonal routes still fail.
    for (let i = 1; i < points.length; i++) {
      const previous = points[i - 1], point = points[i];
      if (Math.abs(point.x - previous.x) < 1e-7) point.x = previous.x;
      else if (Math.abs(point.y - previous.y) < 1e-7) point.y = previous.y;
      else throw new Error('graph_layout_failed');
    }
    return { ...edge, points };
  });
  return { width: result.width, height: result.height, positions, edges: routed };
}

// Retain only the latest project's components. Unchanged components reuse their
// local coordinates; growing groups can move later groups down to avoid overlap.
export function createGraphLayout(engine) {
  let cache = new Map(), previousGroups = [], previousSingles = [];
  return async nodes => {
    const structure = graphStructure(nodes), groups = components(structure);
    const ranks = new Map(previousGroups.flatMap((group, i) => group.map(id => [id, i])));
    const rank = group => Math.min(Infinity, ...group.map(node => ranks.get(node.id) ?? Infinity));
    const connected = groups.filter(group => group.length > 1).sort((a, b) => rank(a) - rank(b) || compare(a[0].id, b[0].id));
    const singleRanks = new Map(previousSingles.map((id, i) => [id, i]));
    const singles = groups.filter(group => group.length === 1).map(group => group[0].id).sort((a, b) => (singleRanks.get(a) ?? Infinity) - (singleRanks.get(b) ?? Infinity) || compare(a, b));
    const nextCache = new Map(), result = emptyLayout(); let top = 0;
    for (const group of connected) {
      const key = JSON.stringify(group), layout = cache.get(key) || await layoutComponent(group, engine);
      nextCache.set(key, layout);
      for (const [id, position] of layout.positions) result.positions.set(id, { x: position.x, y: position.y + top });
      result.edges.push(...layout.edges.map(edge => ({ ...edge, points: edge.points.map(point => ({ x: point.x, y: point.y + top })) })));
      result.width = Math.max(result.width, layout.width); top += layout.height;
      // Each ELK component already has 32px of padding at both vertical ends.
    }
    const columns = connected.length ? Math.min(3, Math.max(1, Math.floor((result.width - 48 + gap) / (nodeWidth + gap)))) : Math.min(3, Math.max(1, singles.length));
    for (const [i, id] of singles.entries()) result.positions.set(id, { x: 24 + i % columns * (nodeWidth + gap), y: top + 32 + Math.floor(i / columns) * (nodeHeight + gap) });
    if (singles.length) {
      result.width = Math.max(result.width, 48 + columns * nodeWidth + (columns - 1) * gap);
      top += componentGap + Math.ceil(singles.length / columns) * (nodeHeight + gap) - gap;
    }
    result.height = Math.max(result.height, top); result.order = [...result.positions.keys()];
    cache = nextCache; previousGroups = connected.map(group => group.map(node => node.id)); previousSingles = singles;
    return result;
  };
}

// Preserve ELK's route, rounding only its existing orthogonal corners.
export function edgePath(route) {
  const points = route.filter((point, i) => !i || point.x !== route[i - 1].x || point.y !== route[i - 1].y);
  let path = `M${points[0].x},${points[0].y}`;
  for (let i = 1; i < points.length - 1; i++) {
    const a = points[i - 1], b = points[i], c = points[i + 1];
    if (a.x === b.x && b.x === c.x || a.y === b.y && b.y === c.y) { path += ` L${b.x},${b.y}`; continue; }
    const before = Math.hypot(b.x - a.x, b.y - a.y), after = Math.hypot(c.x - b.x, c.y - b.y), radius = Math.min(16, before / 2, after / 2);
    path += ` L${b.x + (a.x - b.x) * radius / before},${b.y + (a.y - b.y) * radius / before} Q${b.x},${b.y} ${b.x + (c.x - b.x) * radius / after},${b.y + (c.y - b.y) * radius / after}`;
  }
  const last = points.at(-1); return path + ` L${last.x},${last.y}`;
}
export function shortIdentity(account) { return account.display_name + ' · ' + account.account_id.slice(-6); }
export function sourceIdentity(source) { return source.session_id || source.server_session_id; }
export function sourceKey(node) { return node.id + ':' + sourceIdentity(node.source) + ':' + node.source.version; }
