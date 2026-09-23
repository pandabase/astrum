import { useState, type ReactNode } from "react";
import { Form, Link, NavLink, useLocation, useNavigation } from "react-router";
import { useApiKey } from "~/lib/auth";
import { isAdmin } from "~/lib/session";
import { ThemeControl } from "./theme-control";
import { navigation } from "~/nav";

export function AppShell({ children }: { children: ReactNode }) {
  const key = useApiKey();
  const { pathname } = useLocation();
  const navigationState = useNavigation();
  const [menuOpen, setMenuOpen] = useState(false);
  const groups = navigation
    .map((group) => ({ ...group, items: group.items.filter((item) => !item.role || isAdmin(key.role)) }))
    .filter((group) => group.items.length > 0);
  const current = groups.flatMap((group) => group.items).find((item) =>
    item.to === "/" ? pathname === "/" : pathname.startsWith(item.to) || item.alsoActive?.some((prefix) => pathname.startsWith(prefix)),
  );

  return (
    <div className="min-h-dvh md:grid md:grid-cols-[14rem_minmax(0,1fr)]">
      <a href="#main-content" className="sr-only focus:not-sr-only focus:fixed focus:top-3 focus:left-3 focus:z-50 focus:bg-canvas focus:p-3">
        Skip to content
      </a>
      <aside className="border-b border-line bg-sunken md:sticky md:top-0 md:flex md:h-dvh md:flex-col md:overflow-y-auto md:border-r md:border-b-0">
        <div className="flex h-16 shrink-0 items-center justify-between border-b border-line px-5">
          <Link to="/" onClick={() => setMenuOpen(false)} className="text-lg font-semibold tracking-tight">astrum<span className="text-muted"> /</span></Link>
          <button
            type="button"
            aria-expanded={menuOpen}
            aria-controls="sidebar-navigation"
            onClick={() => setMenuOpen(!menuOpen)}
            className="min-h-9 border border-line-strong px-3 text-xs md:hidden"
          >
            {menuOpen ? "Close menu" : "Menu"}
          </button>
        </div>
        <div id="sidebar-navigation" className={`${menuOpen ? "flex" : "hidden"} flex-1 flex-col md:flex`}>
          <nav aria-label="Main" className="grid flex-1 content-start gap-6 px-3 py-5">
            {groups.map((group, i) => (
              <div key={group.label ?? i} className="grid gap-0.5">
                {group.label && <p className="px-3 pb-2 text-[11px] font-medium tracking-wider text-muted uppercase">{group.label}</p>}
                {group.items.map((item) => (
                  <NavLink
                    key={item.to}
                    to={item.to}
                    end={item.to === "/"}
                    onClick={() => setMenuOpen(false)}
                    className={({ isActive }) =>
                      `border-l-2 px-3 py-2 transition-colors motion-reduce:transition-none ${
                        isActive || item.alsoActive?.some((prefix) => pathname.startsWith(prefix))
                          ? "border-ink bg-canvas font-medium"
                          : "border-transparent text-muted hover:bg-canvas hover:text-ink"
                      }`
                    }
                  >
                    {item.label}
                  </NavLink>
                ))}
              </div>
            ))}
          </nav>
          <div className="flex items-center justify-between gap-3 border-t border-line p-5">
            <div className="min-w-0">
              <p className="truncate font-medium">{key.name}</p>
              <p className="mt-1 text-xs text-muted">{key.role} access</p>
            </div>
            <Form method="post" action="/sign-out">
              <button type="submit" className="min-h-9 whitespace-nowrap text-xs text-muted underline-offset-4 hover:text-ink hover:underline">
                Sign out
              </button>
            </Form>
          </div>
        </div>
      </aside>
      <div className="min-w-0">
        <div className="relative flex h-12 items-center justify-between gap-3 border-b border-line px-5 text-xs md:h-16 md:px-8">
          <p><span className="text-muted">Workspace</span><span aria-hidden="true" className="mx-3 text-line-strong">/</span>{current?.label ?? "Astrum"}</p>
          <div className="flex items-center gap-4">
            <span role="status" className="text-muted">{navigationState.state === "submitting" ? "Saving…" : navigationState.state === "loading" ? "Loading…" : ""}</span>
            <ThemeControl />
          </div>
          {navigationState.state !== "idle" && <div aria-hidden="true" className="absolute inset-x-0 bottom-0 h-px bg-ink motion-safe:animate-pulse" />}
        </div>
        <main aria-busy={navigationState.state !== "idle"} id="main-content" tabIndex={-1} className="mx-auto max-w-[100rem] p-5 md:p-8 lg:p-10">{children}</main>
      </div>
    </div>
  );
}
