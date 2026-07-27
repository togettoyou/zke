import { create } from "zustand";

export type Theme = "light" | "dark";

export const THEME_STORAGE_KEY = "zke.theme";

function readInitialTheme(): Theme {
  // index.html already resolved the theme before first paint; mirror it so the
  // store and the DOM never disagree.
  const applied = document.documentElement.dataset.theme;
  return applied === "dark" ? "dark" : "light";
}

function applyTheme(theme: Theme): void {
  document.documentElement.dataset.theme = theme;
  try {
    localStorage.setItem(THEME_STORAGE_KEY, theme);
  } catch {
    // Private browsing modes can reject storage; the theme still applies.
  }
}

type ThemeState = {
  theme: Theme;
  setTheme: (theme: Theme) => void;
  toggleTheme: () => void;
};

export const useThemeStore = create<ThemeState>((set, get) => ({
  theme: readInitialTheme(),
  setTheme: (theme) => {
    applyTheme(theme);
    set({ theme });
  },
  toggleTheme: () => {
    get().setTheme(get().theme === "dark" ? "light" : "dark");
  },
}));
