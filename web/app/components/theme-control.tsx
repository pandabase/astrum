import { useEffect, useState } from "react";

export type Theme = "system" | "light" | "dark";

export function readTheme(): Theme {
  try {
    const theme = localStorage.getItem("astrum.theme");
    return theme === "light" || theme === "dark" ? theme : "system";
  } catch {
    return "system";
  }
}

export function ThemeControl() {
  const [theme, setTheme] = useState<Theme>("system");

  useEffect(() => {
    const sync = () => {
      const value = readTheme();
      setTheme(value);
      document.documentElement.dataset.theme = value;
    };
    sync();
    window.addEventListener("storage", sync);
    return () => window.removeEventListener("storage", sync);
  }, []);

  function change(value: Theme) {
    setTheme(value);
    document.documentElement.dataset.theme = value;
    try {
      localStorage.setItem("astrum.theme", value);
    } catch {
      // Keep the selection usable when browser storage is unavailable.
    }
  }

  return (
    <label className="flex items-center gap-2 text-xs text-muted">
      <span className="sr-only sm:not-sr-only">Theme</span>
      <select
        aria-label="Theme"
        value={theme}
        onChange={(event) => change(event.target.value as Theme)}
        className="min-h-9 border border-line bg-canvas px-2 text-ink"
      >
        <option value="system">System</option>
        <option value="light">Light</option>
        <option value="dark">Dark</option>
      </select>
    </label>
  );
}
