// getRandomValues remains available on trusted-network HTTP pages, unlike
// randomUUID, SubtleCrypto and the asynchronous clipboard API.
export function requestID() {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 15) | 64;
  bytes[8] = (bytes[8] & 63) | 128;
  const hex = Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

export async function sha256(bytes) {
  const digest = crypto.subtle
    ? new Uint8Array(await crypto.subtle.digest('SHA-256', bytes))
    : (await import('./vendor/noble-sha256-1.8.0.mjs')).sha256(bytes);
  return Array.from(digest, b => b.toString(16).padStart(2, '0')).join('');
}

export async function copyToClipboard(text) {
  if (navigator.clipboard?.writeText) return navigator.clipboard.writeText(text);
  const previous = document.activeElement;
  const input = document.createElement('textarea');
  input.value = text;
  input.style.cssText = 'position:fixed;opacity:0;pointer-events:none';
  (document.querySelector('dialog[open]') || document.body).append(input);
  try {
    input.select();
    if (!document.execCommand('copy')) throw new Error('clipboard_unavailable');
  } finally {
    input.remove();
    previous?.focus({ preventScroll: true });
  }
}
