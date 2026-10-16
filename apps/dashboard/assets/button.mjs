import { icon } from './icons.mjs';

// Action controls share presentation; navigation and content cards keep their
// own layout. Updating labels must never discard the leading icon.
export function setButtonLabel(node, label) {
  const text = node.querySelector('.button-label');
  if (text) text.textContent = label;
  else node.textContent = label;
}

export function styleButton(node, { glyph, size = 'regular' } = {}) {
  const label = node.textContent;
  node.classList.add('ui-button');
  node.dataset.size = size;
  node.replaceChildren();
  if (glyph && size !== 'compact') node.append(icon(glyph));
  const text = document.createElement('span'); text.className = 'button-label'; text.textContent = label;
  node.append(text);
  return node;
}

export function createButton(label, { glyph, size, className = '', type = 'button', onClick } = {}) {
  const node = document.createElement('button');
  node.className = className; node.type = type; node.textContent = label;
  styleButton(node, { glyph, size });
  if (onClick) node.addEventListener('click', onClick);
  return node;
}
