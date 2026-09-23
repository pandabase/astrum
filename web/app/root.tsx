import "@fontsource-variable/inter";
import { Links, Meta, Outlet, Scripts, ScrollRestoration } from "react-router";
import { RouteError } from "./components/route-error";
import { LoadingPage } from "./components/loading";
import { readTheme } from "./components/theme-control";
import "./app.css";

export const meta = () => [{ title: "Astrum" }];

export function Layout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" suppressHydrationWarning>
      <head>
        <meta charSet="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1" />
        <meta name="referrer" content="no-referrer" />
        <script dangerouslySetInnerHTML={{ __html: `document.documentElement.dataset.theme=(${readTheme.toString()})();` }} />
        <Meta />
        <Links />
      </head>
      <body>
        {children}
        <ScrollRestoration />
        <Scripts />
      </body>
    </html>
  );
}

export default function App() {
  return <Outlet />;
}

export function HydrateFallback() {
  return <LoadingPage />;
}

export function ErrorBoundary() {
  return (
    <main className="mx-auto max-w-xl p-6">
      <RouteError />
    </main>
  );
}
