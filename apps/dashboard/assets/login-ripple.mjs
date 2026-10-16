// A fixed typographic surface: energy travels; glyph positions never move.
// The animation has no network, account, project or workspace dependencies.
const clamp = (n, a = 0, b = 1) => Math.max(a, Math.min(b, n));
const smooth = (a, b, value) => { const t = clamp((value - a) / (b - a)); return t * t * (3 - 2 * t); };
const hash = (x, y) => {
  let n = Math.imul(x + 127, 374761393) ^ Math.imul(y + 311, 668265263);
  n = Math.imul(n ^ (n >>> 13), 1274126177);
  return ((n ^ (n >>> 16)) >>> 0) / 4294967296;
};
// Overlapping pools vary the mark's density without giving every point on a
// wavefront the same symbol. Each cell owns a seeded, stable little alphabet.
const pools = [
  ['.', ',', ':', ';', "'", '`', '-', '_'],
  [':', ';', '!', '|', '/', '\\', '-', '_', '~', '^', 'i', 'l'],
  ['=', '+', 'x', '<', '>', '/', '\\', '(', ')', '[', ']', '{', '}', '~', ':', '?'],
  ['+', '*', 'o', '0', '%', '#', '&', '@', '=', '<', '>', '[', ']', '{', '}', '^'],
];
const glyphs = [...new Set(pools.flat())];
const poolIndices = pools.map(pool => pool.map(glyph => glyphs.indexOf(glyph)));
const weights = [400, 500, 600];
const drops = [
  { x:.45, y:.54, phase:.4 },
  { x:.62, y:.41, phase:2.3 },
  { x:.37, y:.59, phase:4.2 },
  { x:.55, y:.52, phase:5.6 },
];
const interval = 3;
const intervalJitter = 1.5;
const lifetime = 18.8;
// Preserve the approved opening radius while increasing subsequent motion 30%.
const openingTime = 7.2 / 1.3;
const speedMultiplier = 2.25 * 1.3;
const speed = 29 * speedMultiplier;
const bandScale = 2 / 3;
// Keep the entire group narrower as it speeds up, including gaps between echoes.
const echoDelay = 1.65 * bandScale / speedMultiplier;

// Sample each birth interval once, from a stable pseudorandom sequence. Keep
// every living group; rewinding (including reduced motion) rebuilds the same scene.
export function createRippleSchedule() {
  let groups = [], nextBirth = 0, sequence = 0, previousTime = -1;
  return time => {
    if (time < previousTime) { groups = []; nextBirth = 0; sequence = 0; }
    previousTime = time;
    groups = groups.filter(group => time - group.born <= lifetime);
    while (nextBirth <= time) {
      if (time - nextBirth <= lifetime) groups.push({ sequence, born:nextBirth });
      nextBirth += interval + (hash(sequence, 1729) * 2 - 1) * intervalJitter;
      sequence++;
    }
    return groups;
  };
}

export function mountLoginRipple(canvas) {
  const ctx = canvas.getContext('2d', { alpha:true });
  if (!ctx || typeof ResizeObserver === 'undefined') return;
  const login = canvas.closest('#login');
  const parent = canvas.parentElement;
  const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');
  const groupsAt = createRippleSchedule();
  let width = 0, height = 0, dpr = 1, scale = 1;
  let cells = [], atlas, spriteWidth, spriteHeight;
  let raf = 0, lastTick = null, lastDraw = 0, elapsed = openingTime;
  let disposed = false, pageActive = true;

  function makeAtlas() {
    // Pre-rasterize each real character/weight once at display resolution.
    // Subtle changes in energy blend adjacent glyphs instead of flickering.
    const size = clamp(12.5 * scale, 11.5, 15);
    spriteWidth = Math.ceil(24 * dpr); spriteHeight = Math.ceil(30 * dpr);
    atlas = document.createElement('canvas');
    atlas.width = spriteWidth * glyphs.length; atlas.height = spriteHeight * weights.length;
    const ink = atlas.getContext('2d');
    ink.fillStyle = '#343434'; ink.textAlign = 'center'; ink.textBaseline = 'middle';
    weights.forEach((weight, row) => {
      ink.font = `${weight} ${size * dpr}px "SFMono-Regular", Menlo, Consolas, monospace`;
      glyphs.forEach((glyph, column) => ink.fillText(glyph, (column + .5) * spriteWidth, (row + .5) * spriteHeight));
    });
  }

  function measure() {
    const bounds = parent.getBoundingClientRect();
    const nextWidth = Math.round(bounds.width), nextHeight = Math.round(bounds.height);
    const nextDpr = Math.min(devicePixelRatio || 1, 2);
    if (nextWidth === width && nextHeight === height && nextDpr === dpr) return;
    width = nextWidth; height = nextHeight; dpr = nextDpr;
    if (!width || !height) { reconcile(); return; }
    canvas.width = Math.round(width * dpr); canvas.height = Math.round(height * dpr);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    scale = clamp(Math.min(width / 1050, height / 900), .67, 1.45);
    makeAtlas();
    const stepX = clamp(15 * scale, 12, 19), stepY = clamp(18 * scale, 15, 23);
    const columns = Math.floor(width / stepX), rows = Math.floor(height / stepY);
    const offsetX = (width - (columns - 1) * stepX) / 2, offsetY = (height - (rows - 1) * stepY) / 2;
    cells = [];
    for (let row = 0; row < rows; row++) for (let col = 0; col < columns; col++) {
      const x = offsetX + col * stepX, y = offsetY + row * stepY;
      const edge = smooth(18, width * .13, x) * smooth(18, width * .15, width - x)
        * smooth(70, height * .24, y) * smooth(32, height * .15, height - y);
      if (edge < .002) continue;
      const seed = hash(col, row), seed2 = hash(col + 913, row + 617);
      const fields = drops.map(drop => {
        const dx = x - width * drop.x, dy = y - height * drop.y;
        const angle = Math.atan2(dy, dx), radius = Math.hypot(dx, dy);
        return { radius, angle,
          bend:(Math.sin(angle * 3 + drop.phase) * 5 + Math.sin(angle * 7 - drop.phase) * 2.4) * scale,
          texture:.78 + .14 * Math.sin(angle * 4 + radius * .009 + drop.phase) + .08 * Math.cos(angle * 9 - drop.phase),
        };
      });
      const palette = poolIndices.map((pool, index) => pool[Math.floor(hash(col + index * 719, row + index * 331) * pool.length)]);
      cells.push({ x, y, seed, seed2, edge, fields, palette, base:seed > .86 ? .035 + seed2 * .017 : 0 });
    }
    draw(); reconcile();
  }

  function glyph(index, weight, alpha, x, y) {
    if (alpha < .002) return;
    ctx.globalAlpha = Math.min(alpha, .48);
    ctx.drawImage(atlas, index * spriteWidth, weight * spriteHeight, spriteWidth, spriteHeight,
      x - spriteWidth / dpr / 2, y - spriteHeight / dpr / 2, spriteWidth / dpr, spriteHeight / dpr);
  }

  function draw() {
    if (disposed || login.hidden || document.hidden || !pageActive || !width || !height || !atlas) return;
    ctx.clearRect(0, 0, width, height);
    const time = reducedMotion.matches ? openingTime : elapsed;
    const waves = [];
    for (const { sequence, born } of groupsAt(time)) {
      const age = time - born;
      const dropIndex = sequence % drops.length;
      const strength = smooth(0, 1, age) * (1 - smooth(10.8, lifetime, age));
      for (let echo = 0; echo < 4; echo++) {
        const travel = age - echo * echoDelay;
        if (travel <= 0) continue;
        const radius = travel * speed * scale;
        const spread = (8.2 + radius / scale * .012) * scale * bandScale;
        waves.push({ dropIndex, radius, spread, strength:strength * Math.pow(.78, echo) * smooth(0, .9, travel)
          / (1 + radius / scale * .00065) });
      }
    }
    for (const cell of cells) {
      let energy = 0;
      for (const wave of waves) {
        const field = cell.fields[wave.dropIndex];
        const distance = field.radius + field.bend - wave.radius;
        if (Math.abs(distance) > wave.spread * 4.5) continue;
        const band = distance / wave.spread;
        const profile = Math.exp(-band * band * .5) + .1 * Math.exp(-band * band * .075);
        energy += profile * wave.strength * field.texture;
      }
      // Fixed, spatial grain creates broken arcs without frame-to-frame randomness.
      energy = clamp(energy * (.64 + cell.seed * .36));
      const ink = Math.pow(energy, .82) * .49 * cell.edge;
      if (ink < .008) { if (cell.base) glyph(0, 0, cell.base * cell.edge, cell.x, cell.y); continue; }
      const level = clamp(energy * 3.8 + cell.seed2 * .45 - .15, 0, pools.length - 1);
      const low = Math.floor(level), blend = smooth(.15, .85, level - low);
      const weightLevel = smooth(.25, .88, energy) * 2;
      const weight = Math.floor(weightLevel), weightBlend = weightLevel - weight;
      for (let variant = 0; variant < 2; variant++) {
        const row = Math.min(weight + variant, weights.length - 1);
        const opacity = ink * (variant ? weightBlend : 1 - weightBlend);
        glyph(cell.palette[low], row, opacity * (1 - blend), cell.x, cell.y);
        if (low < pools.length - 1) glyph(cell.palette[low + 1], row, opacity * blend, cell.x, cell.y);
      }
    }
    ctx.globalAlpha = 1;
    if (!parent.hasAttribute('data-ripple-ready')) parent.dataset.rippleReady = '';
  }

  function canAnimate() { return !disposed && !login.hidden && !reducedMotion.matches && pageActive && !document.hidden && width > 0 && height > 0; }
  function tick(now) {
    raf = 0;
    if (!canAnimate()) { lastTick = null; return; }
    if (lastTick !== null) elapsed += Math.min((now - lastTick) / 1000, .1);
    lastTick = now;
    if (now - lastDraw >= 1000 / 30) { draw(); lastDraw = now - (now - lastDraw) % (1000 / 30); }
    raf = requestAnimationFrame(tick);
  }
  function reconcile() {
    if (!canAnimate()) {
      if (raf) cancelAnimationFrame(raf);
      raf = 0; lastTick = null;
    } else if (!raf) { lastTick = null; raf = requestAnimationFrame(tick); }
  }
  function motionChanged() { draw(); reconcile(); }
  function hide() { pageActive = false; reconcile(); }
  function show() { pageActive = true; measure(); reconcile(); }
  const observer = new ResizeObserver(measure);
  observer.observe(parent);
  const visibilityChanged = () => { if (!document.hidden) draw(); reconcile(); };
  const loginObserver = new MutationObserver(() => { if (!login.hidden) measure(); reconcile(); });
  loginObserver.observe(login, { attributes:true, attributeFilter:['hidden'] });
  document.addEventListener('visibilitychange', visibilityChanged);
  window.addEventListener('pagehide', hide);
  window.addEventListener('pageshow', show);
  window.addEventListener('resize', measure);
  reducedMotion.addEventListener('change', motionChanged);
  measure();
  function destroy() {
    disposed = true; reconcile(); observer.disconnect(); loginObserver.disconnect();
    document.removeEventListener('visibilitychange', visibilityChanged);
    window.removeEventListener('pagehide', hide); window.removeEventListener('pageshow', show);
    window.removeEventListener('resize', measure); reducedMotion.removeEventListener('change', motionChanged);
    cells = []; atlas = null; ctx.clearRect(0, 0, width, height);
    delete parent.dataset.rippleReady;
  }
  return { destroy };
}

// Decorative content loads independently of authentication. If rendering is
// unavailable the existing dot pattern remains, and signing in still works.
const canvas = globalThis.document?.getElementById('login-ripple');
if (canvas) mountLoginRipple(canvas);
