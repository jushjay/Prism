import { useSyncExternalStore } from 'react';

export type ThemeMode = 'dark' | 'light';

const THEME_KEY = 'prism-theme';

const readStored = (): ThemeMode => {
  if (typeof window === 'undefined') return 'dark';
  return localStorage.getItem(THEME_KEY) === 'light' ? 'light' : 'dark';
};

let currentTheme: ThemeMode = readStored();
const listeners = new Set<() => void>();

const notify = () => listeners.forEach((l) => l());

export function getTheme(): ThemeMode {
  return currentTheme;
}

export function setTheme(theme: ThemeMode) {
  if (theme === currentTheme) return;
  currentTheme = theme;
  if (typeof window !== 'undefined') {
    localStorage.setItem(THEME_KEY, theme);
    document.documentElement.setAttribute('data-theme', theme);
  }
  notify();
}

export function toggleTheme() {
  setTheme(currentTheme === 'dark' ? 'light' : 'dark');
}

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => {
    listeners.delete(cb);
  };
}

export function useTheme(): ThemeMode {
  return useSyncExternalStore(subscribe, getTheme, getTheme);
}

// ensure attribute is set as early as possible
if (typeof window !== 'undefined') {
  document.documentElement.setAttribute('data-theme', currentTheme);
}
