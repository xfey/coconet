# README illustrations

[index.html](index.html) is the editable, local preview: three scenes in English
and Chinese, keyboard controls and reduced-motion support. Every project, author
and conversation in the illustrations is fictional.

The root README uses `promo-en.gif` / `promo-zh.gif` (1296 × 668, about 131 KiB
and 127 KiB). Each of the three frames remains visible for 6.5 seconds. The PNG
files provide static alternatives; the HTML preview also has a pause control.
GitHub renders the GIF rather than the interactive HTML carousel.

The illustrations keep narrow outer margins. A plain light-gray canvas separates
the work graph from the explanation and example chat on the left.

The six PNGs and both GIFs are generated from the HTML, CSS and JavaScript here.
To regenerate, use Node.js, Python 3 with Pillow, and a separate Playwright install:

```sh
COCONET_PLAYWRIGHT_MODULE=/absolute/path/to/playwright/index.mjs node docs/assets/readme/render.mjs
python3 docs/assets/readme/navigation.py
```

`COCONET_BROWSER_EXECUTABLE` optionally selects a compatible Chromium executable.
Rendering checks that node titles and chat bubbles fit, waits for the local font
and images, and rejects unexpected remote requests. No Agent or model is invoked.

Navigation SVGs combine icons with visible labels, using local vector paths and
system fonts without scripts or external assets. Website and language links use
168 × 48 images; Docs, Contributing and MIT License use 168 × 60 images.
README links retain descriptive alt text.
Agent mark sources and the MIT license are in [agent-marks](../agent-marks/README.md).
The brand font uses the existing [Outfit Brand subset](../../../apps/dashboard/assets/fonts/README.md).
