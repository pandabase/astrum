import { Outlet } from "react-router";
import { AppShell } from "~/components/app-shell";
import { KeyProvider, requireKey } from "~/lib/auth";
import { sessionContext } from "~/lib/session";
import type { Route } from "./+types/app-layout";

const auth: Route.ClientMiddlewareFunction = async ({ request, context }) => {
  context.set(sessionContext, await requireKey(request));
};

export const clientMiddleware: Route.ClientMiddlewareFunction[] = [auth];

export async function clientLoader({ context }: Route.ClientLoaderArgs) {
  return { key: context.get(sessionContext) };
}

export default function AppLayout({ loaderData }: Route.ComponentProps) {
  return (
    <KeyProvider value={loaderData.key}>
      <AppShell>
        <Outlet />
      </AppShell>
    </KeyProvider>
  );
}
