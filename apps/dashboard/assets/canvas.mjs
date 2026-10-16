export const minZoom = .25, maxZoom = 2;
const topInset = 80, bottomInset = 96;
const clampZoom = zoom => Math.max(minZoom, Math.min(maxZoom, zoom));

export function zoomAt(camera, zoom, anchor) {
  const next = clampZoom(zoom), ratio = next / camera.zoom;
  return { x: anchor.x - (anchor.x - camera.x) * ratio, y: anchor.y - (anchor.y - camera.y) * ratio, zoom: next };
}

export function fitCanvas(bounds, viewport) {
  const zoom = clampZoom(Math.min(1, (viewport.width - 64) / bounds.width, (viewport.height - topInset - bottomInset) / bounds.height));
  return { zoom, x: Math.max(32, (viewport.width - bounds.width * zoom) / 2), y: Math.max(topInset, topInset + (viewport.height - topInset - bottomInset - bounds.height * zoom) / 2) };
}

// One camera moves DAG nodes and edges; Library stays in the viewport. Pointer work
// updates only a transform; it never rebuilds the graph or changes server data.
export function createCanvas({ viewport, world, onZoom, onBackground, rightInset = () => 0 }) {
  let camera = { x: 40, y: topInset, zoom: 1 }, bounds = { width: 720, height: 400 };
  const pointers = new Map();
  let gesture, moved = false, distance = 0;
  const point = event => { const rect = viewport.getBoundingClientRect(); return { x: event.clientX - rect.left, y: event.clientY - rect.top }; };
  const center = () => ({ x: viewport.clientWidth / 2, y: viewport.clientHeight / 2 });
  function render() {
    world.style.transform = `translate(${camera.x}px, ${camera.y}px) scale(${camera.zoom})`;
    viewport.style.backgroundPosition = `${camera.x}px ${camera.y}px`;
    viewport.style.backgroundSize = `${24 * camera.zoom}px ${24 * camera.zoom}px`;
    onZoom(camera.zoom);
  }
  function measure() {
    const values = [...pointers.values()];
    const a = values[0], b = values[1] || a;
    return { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2, distance: Math.hypot(a.x - b.x, a.y - b.y) };
  }
  viewport.addEventListener('pointerdown', event => {
    if (event.button !== 0 || event.target.closest('button, a, input, summary')) return;
    pointers.set(event.pointerId, point(event)); gesture = measure(); moved = pointers.size > 1; distance = 0;
    viewport.setPointerCapture(event.pointerId);
    viewport.classList.add('dragging'); viewport.focus({ preventScroll: true });
  });
  viewport.addEventListener('pointermove', event => {
    if (!pointers.has(event.pointerId)) return;
    pointers.set(event.pointerId, point(event));
    const next = measure(), dx = next.x - gesture.x, dy = next.y - gesture.y;
    distance += Math.hypot(dx, dy);
    if (distance > 4) moved = true;
    if (pointers.size > 1 && gesture.distance > 0) camera = zoomAt(camera, camera.zoom * next.distance / gesture.distance, gesture);
    camera.x += dx; camera.y += dy; gesture = next; render();
  });
  function end(event) {
    if (!pointers.has(event.pointerId)) return;
    pointers.delete(event.pointerId);
    if (pointers.size) { gesture = measure(); return; }
    viewport.classList.remove('dragging');
    if (!moved && event.type === 'pointerup') onBackground();
  }
  viewport.addEventListener('pointerup', end);
  viewport.addEventListener('pointercancel', end);
  viewport.addEventListener('lostpointercapture', end);
  viewport.addEventListener('wheel', event => {
    if (event.target.closest('button, input')) return;
    event.preventDefault();
    const unit = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? viewport.clientHeight : 1;
    if (event.ctrlKey || event.metaKey) camera = zoomAt(camera, camera.zoom * Math.exp(-event.deltaY * unit * .006), point(event));
    else { camera.x -= event.deltaX * unit; camera.y -= event.deltaY * unit; }
    render();
  }, { passive: false });
  const control = {
    setBounds(next) { bounds = next; },
    reset() { pointers.clear(); viewport.classList.remove('dragging'); camera = { x: 40, y: topInset, zoom: 1 }; render(); },
    zoom(value) { camera = zoomAt(camera, value, center()); render(); },
    step(delta) { control.zoom(Math.round((camera.zoom + delta) * 100) / 100); },
    fit() { camera = fitCanvas(bounds, { width: viewport.clientWidth - rightInset(), height: viewport.clientHeight }); render(); },
    reveal(rect, rightInset = 0) {
      const availableWidth = viewport.clientWidth - rightInset;
      const x = camera.x + rect.x * camera.zoom, y = camera.y + rect.y * camera.zoom;
      const w = rect.width * camera.zoom, h = rect.height * camera.zoom;
      if (x < 16 || x + w > availableWidth - 16) camera.x += availableWidth / 2 - x - w / 2;
      if (y < topInset || y + h > viewport.clientHeight - bottomInset) camera.y += topInset + (viewport.clientHeight - topInset - bottomInset) / 2 - y - h / 2;
      render();
    },
  };
  viewport.addEventListener('keydown', event => {
    if (event.target !== viewport) return;
    const direction = { ArrowLeft: [64, 0], ArrowRight: [-64, 0], ArrowUp: [0, 64], ArrowDown: [0, -64] }[event.key];
    if (direction) { camera.x += direction[0]; camera.y += direction[1]; render(); }
    else if (event.key === '+' || event.key === '=') control.step(.1);
    else if (event.key === '-') control.step(-.1);
    else if (event.key === '0') control.fit();
    else return;
    event.preventDefault();
  });
  render();
  return control;
}
