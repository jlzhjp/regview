import htmx from 'htmx.org';
import './app.css';

declare global {
  interface Window {
    htmx: typeof htmx;
  }
}

// Keep HTMX available for extensions and browser debugging.
window.htmx = htmx;

type Theme = 'system' | 'light' | 'dark';

function isTheme(value: string | null): value is Theme {
  return value === 'system' || value === 'light' || value === 'dark';
}

const themeSelect = document.querySelector<HTMLSelectElement>('#theme');
const systemTheme = window.matchMedia('(prefers-color-scheme: dark)');
let preference: Theme = 'system';

try {
  const saved = localStorage.getItem('regview-theme');
  if (isTheme(saved)) preference = saved;
} catch {
  // Theme selection still works when browser storage is unavailable.
}

function applyTheme() {
  document.documentElement.dataset.theme =
    preference === 'system' ? (systemTheme.matches ? 'dark' : 'light') : preference;
}

applyTheme();
systemTheme.addEventListener('change', applyTheme);

if (themeSelect) {
  themeSelect.value = preference;
  themeSelect.addEventListener('change', () => {
    if (!isTheme(themeSelect.value)) return;
    preference = themeSelect.value;
    applyTheme();
    try {
      localStorage.setItem('regview-theme', preference);
    } catch {
      // Storage is optional.
    }
  });
}

function setStatus(message: string): void {
  const status = document.querySelector<HTMLParagraphElement>('#request-status');
  if (status) status.textContent = message;
}

document.body.addEventListener('htmx:responseError', () => {
  setStatus('Request failed. Please try again.');
});
document.body.addEventListener('htmx:sendError', () => {
  setStatus('Could not reach the server. Please try again.');
});
document.body.addEventListener('htmx:beforeRequest', () => {
  setStatus('');
});
