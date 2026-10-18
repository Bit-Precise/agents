import {
  createContext,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";

// The shadcn/ui Vite dark-mode provider: a class on <html>, remembered locally.
type Theme = "dark" | "light" | "system";
type ThemeState = {
  theme: Theme;
  resolvedTheme: "dark" | "light";
  setTheme: (theme: Theme) => void;
};

const storageKey = "agents-ui-theme";
const ThemeContext = createContext<ThemeState>({
  theme: "system",
  resolvedTheme: "light",
  setTheme: () => {},
});

function stored(): Theme {
  try {
    const value = localStorage.getItem(storageKey);
    return value === "dark" || value === "light" ? value : "system";
  } catch {
    return "system";
  }
}
function system(): "dark" | "light" {
  return window.matchMedia("(prefers-color-scheme: dark)").matches
    ? "dark"
    : "light";
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(stored);
  const [preferred, setPreferred] = useState(system);
  useEffect(() => {
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const change = () => setPreferred(system());
    media.addEventListener("change", change);
    return () => media.removeEventListener("change", change);
  }, []);
  const resolvedTheme = theme === "system" ? preferred : theme;
  useEffect(() => {
    const root = document.documentElement;
    root.classList.remove("light", "dark");
    root.classList.add(resolvedTheme);
  }, [resolvedTheme]);
  const setTheme = (next: Theme) => {
    try {
      localStorage.setItem(storageKey, next);
    } catch {}
    setThemeState(next);
  };
  return (
    <ThemeContext.Provider value={{ theme, resolvedTheme, setTheme }}>
      {children}
    </ThemeContext.Provider>
  );
}

export function useTheme() {
  return useContext(ThemeContext);
}
