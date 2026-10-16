import { createButton } from './button.mjs';
import { icon } from './icons.mjs';

export function dialogActions(...children) {
  const row = document.createElement('div'); row.className = 'dialog-actions';
  row.append(...children); return row;
}

export function commandBlock(command, { element, t, copyText, label = 'copy_command', buttonClass = 'primary' }) {
  const block = element('div', undefined, 'command-block');
  const pre = element('pre'); pre.append(element('code', command));
  const copy = createButton(t(label), { glyph: 'copy', className: buttonClass, onClick: () => copyText(command) });
  block.append(pre, dialogActions(copy)); return block;
}

export function disclosure(label, { element, key, opened = false }) {
  const details = element('details', undefined, 'dialog-disclosure');
  details.dataset.disclosure = key; details.open = opened;
  const summary = element('summary'); summary.append(element('span', label), icon('chevronDown'));
  const body = element('div', undefined, 'dialog-section disclosure-body');
  details.append(summary, body);
  return { details, body };
}
