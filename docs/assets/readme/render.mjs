// Regenerate the six PNG frames and two README GIFs from the editable preview.
import assert from 'node:assert/strict';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { spawnSync } from 'node:child_process';

const modulePath = process.env.COCONET_PLAYWRIGHT_MODULE;
if (!modulePath) throw new Error('Set COCONET_PLAYWRIGHT_MODULE to Playwright’s index.mjs.');
const { chromium } = await import(pathToFileURL(modulePath));
const browser = await chromium.launch({ headless: true, ...(process.env.COCONET_BROWSER_EXECUTABLE ? { executablePath: process.env.COCONET_BROWSER_EXECUTABLE } : {}) });
const root = new URL('./', import.meta.url);
const errors = [];
try {
  const page = await browser.newPage({ viewport: { width: 1496, height: 960 }, reducedMotion: 'reduce', deviceScaleFactor: 1 });
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => { if (/^https?:/.test(request.url())) errors.push('Unexpected external request: ' + request.url()); });
  for (const language of ['zh', 'en']) for (const scene of ['graph', 'query', 'fork']) {
    const url = new URL('index.html', root); url.search = new URLSearchParams({ lang: language, scene, autoplay: '0' });
    await page.goto(url.href);
    await page.evaluate(() => document.fonts.ready);
    await page.waitForFunction(() => [...document.images].every(image => image.complete && image.naturalWidth > 0));
    const failures = await page.evaluate(() => {
      const failures = [], rect = selector => document.querySelector(selector).getBoundingClientRect();
      for (const node of document.querySelectorAll('.work-node:not([hidden])')) {
        const title = node.querySelector('p').getBoundingClientRect(), meta = node.querySelector('.node-meta').getBoundingClientRect();
        if (title.bottom + 5 > meta.top || title.right > node.getBoundingClientRect().right - 10) failures.push('Node title does not fit: ' + node.dataset.node);
      }
      if (!document.querySelector('.conversation-note').hidden) {
        if (rect('#headline').bottom + 12 > rect('.conversation-note').top) failures.push('Headline overlaps chat');
        if (rect('.conversation-note').bottom + 12 > rect('.poster-footer').top) failures.push('Chat overlaps footer');
      }
      return failures;
    });
    assert.deepEqual(failures, [], language + '/' + scene);
    const name = scene === 'graph' ? `promo-${language}.png` : `promo-${scene}-${language}.png`;
    await page.locator('.poster').screenshot({ path: fileURLToPath(new URL(name, root)) });
    console.log('Rendered ' + name);
  }
  assert.deepEqual(errors, []);
} finally { await browser.close(); }

const result = spawnSync('python3', ['-c', `
from pathlib import Path
from PIL import Image
import sys
root=Path(sys.argv[1])
for language in ('zh','en'):
    names=[f'promo-{language}.png',f'promo-query-{language}.png',f'promo-fork-{language}.png']
    frames=[Image.open(root/name).convert('RGB').quantize(colors=128) for name in names]
    target=root/f'promo-{language}.gif'
    frames[0].save(target,save_all=True,append_images=frames[1:],duration=6500,loop=0,disposal=2,optimize=True)
    with Image.open(target) as image:
        assert image.n_frames==3 and image.size==(1440,760)
    print(f'Rendered {target.name}: {target.stat().st_size//1024} KiB')
`, fileURLToPath(root)], { stdio: 'inherit' });
if (result.error) throw result.error;
if (result.status !== 0) process.exit(result.status || 1);
