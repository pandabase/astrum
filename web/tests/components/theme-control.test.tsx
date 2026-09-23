import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readTheme, ThemeControl } from "~/components/theme-control";

beforeEach(() => {
  localStorage.clear();
  delete document.documentElement.dataset.theme;
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  localStorage.clear();
  delete document.documentElement.dataset.theme;
});

describe("theme preference", () => {
  it("defaults to system and ignores unknown saved values", () => {
    expect(readTheme()).toBe("system");
    localStorage.setItem("astrum.theme", "unknown");
    expect(readTheme()).toBe("system");
  });

  it("restores a saved preference before rendering the app", () => {
    localStorage.setItem("astrum.theme", "dark");
    new Function(`document.documentElement.dataset.theme=(${readTheme.toString()})();`)();
    expect(document.documentElement.dataset.theme).toBe("dark");
    render(<ThemeControl />);
    expect((screen.getByRole("combobox", { name: "Theme" }) as HTMLSelectElement).value).toBe("dark");
  });

  it("applies and persists each selection", () => {
    render(<ThemeControl />);
    for (const theme of ["dark", "light", "system"]) {
      fireEvent.change(screen.getByRole("combobox", { name: "Theme" }), { target: { value: theme } });
      expect(document.documentElement.dataset.theme).toBe(theme);
      expect(localStorage.getItem("astrum.theme")).toBe(theme);
    }
  });

  it("follows changes from another tab", () => {
    render(<ThemeControl />);
    localStorage.setItem("astrum.theme", "light");
    fireEvent(window, new StorageEvent("storage", { key: "astrum.theme", newValue: "light" }));
    expect(document.documentElement.dataset.theme).toBe("light");
    expect((screen.getByRole("combobox", { name: "Theme" }) as HTMLSelectElement).value).toBe("light");
  });

  it("still switches themes when storage is blocked", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("Blocked"); });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("Blocked"); });
    expect(readTheme()).toBe("system");
    render(<ThemeControl />);
    fireEvent.change(screen.getByRole("combobox", { name: "Theme" }), { target: { value: "dark" } });
    expect(document.documentElement.dataset.theme).toBe("dark");
  });
});
