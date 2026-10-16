import { createButton } from './button.mjs';
import { icon } from './icons.mjs';

// Persistent processing notices and brief action feedback share one stack.
// A manual popover keeps copy feedback visible above the instruction dialog.
export function createNotifications({ region, viewport, details, library, translate }) {
  const notice = region.querySelector('#notice'), toast = region.querySelector('#toast');
  let timer, actionKey = "", actionHandler, dialogOpen = false;
  function renderAction() {
    notice.querySelector("button")?.remove();
    if (!actionKey || notice.hidden) return;
    notice.append(createButton(translate(actionKey), { size: 'compact', onClick: actionHandler }));
  }
  function position() {
    const rect = viewport.getBoundingClientRect();
    region.style.bottom = 'auto';
    if (dialogOpen && innerWidth <= 760 || !rect.width) {
      region.style.left = 'auto'; region.style.right = '20px';
      region.style.top = dialogOpen && innerWidth <= 760 ? 'auto' : '20px';
      region.style.bottom = dialogOpen && innerWidth <= 760 ? '20px' : 'auto';
      region.style.maxWidth = 'min(360px, calc(100vw - 40px))'; return;
    }
    const panel = details.getBoundingClientRect();
    const besidePanel = !details.hidden && panel.left - rect.left >= 160;
    const right = besidePanel ? panel.left : rect.right;
    const trigger = library.getBoundingClientRect();
    const freeWidth = right - (library.hidden ? rect.left : trigger.right) - 40;
    const belowLibrary = innerWidth <= 760 || freeWidth < 240;
    region.style.left = 'auto';
    region.style.right = Math.max(16, innerWidth - right + 20) + 'px';
    region.style.top = rect.top + (belowLibrary ? 72 : 20) + 'px';
    region.style.maxWidth = Math.max(120, Math.min(360, belowLibrary ? right - rect.left - 40 : freeWidth)) + 'px';
    // On phones the details occupy the full width; retain a small canvas
    // header for notifications instead of drawing them over the details.
    details.style.top = !details.hidden && !besidePanel && !region.hidden
      ? (belowLibrary ? 72 : 20) + region.offsetHeight + 16 + 'px' : '';
  }
  function render(node, key, kind) {
    node.dataset.notificationKey = key;
    node.dataset.kind = kind;
    node.replaceChildren(); node.hidden = !key || node === notice && dialogOpen;
    if (key) {
      const mark = icon(kind === 'processing' ? 'refresh' : kind === 'success' ? 'check' : 'info');
      const label = document.createElement('span'); label.textContent = translate(key);
      node.append(mark, label);
    }
    renderAction();
    region.hidden = notice.hidden && toast.hidden;
    if (region.hidden) {
      if (region.matches(':popover-open')) region.hidePopover();
    } else if (region.showPopover && !region.matches(':popover-open')) region.showPopover();
    position();
  }
  function clearToast() { clearTimeout(timer); render(toast, '', 'success'); }
  const observer = new ResizeObserver(position);
  for (const node of [viewport, details, library, region]) observer.observe(node);
  return {
    modal(open) { dialogOpen = open; render(notice, notice.dataset.notificationKey || '', notice.dataset.kind); },
    action(key, handler) { actionKey = key; actionHandler = handler; renderAction(); position(); },
    notice(key) { render(notice, key, ['graph_processing', 'graph_initializing'].includes(key) ? 'processing' : 'info'); },
    toast(key) {
      clearTimeout(timer);
      render(toast, key, ['copy_success', 'library_saved', 'library_removed', 'authorization_done', 'profile_saved'].includes(key) ? 'success' : 'info');
      if (region.showPopover) { region.hidePopover(); region.showPopover(); }
      timer = setTimeout(clearToast, 4000);
    },
    clearToast,
    position,
    refreshLanguage() { for (const node of [notice, toast]) render(node, node.dataset.notificationKey || '', node.dataset.kind); },
  };
}
