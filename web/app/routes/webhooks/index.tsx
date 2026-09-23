import { PageHeader } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { ButtonLink, TextLink } from "~/components/ui/link";
import { Notice } from "~/components/ui/notice";
import { Table, Td, Th } from "~/components/ui/table";
import { api } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import { canWrite } from "~/lib/session";
import type { List, WebhookEndpoint } from "~/lib/types";
import type { Route } from "./+types/index";

export const meta: Route.MetaFunction = () => title("Webhooks");

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const { cursor } = searchParams(request, []);
  return api<List<WebhookEndpoint>>(withQuery("/v1/webhook_endpoints", { cursor, limit: 50 }), { signal: request.signal });
}

export default function Webhooks({ loaderData: endpoints }: Route.ComponentProps) {
  return (
    <div className="grid gap-6">
      <PageHeader title="Webhooks" actions={canWrite(useApiKey().role) && <ButtonLink to="/webhooks/new" variant="primary">New endpoint</ButtonLink>} />
      <p className="max-w-2xl text-muted">
        Endpoints receive events at least once, signed with <code className="font-mono">Astrum-Signature</code>, and failed deliveries are retried for
        about three days.
      </p>
      {endpoints.data.length === 0 ? (
        <Notice>No endpoints yet.</Notice>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>URL</Th>
              <Th>Events</Th>
              <Th>Status</Th>
            </tr>
          </thead>
          <tbody>
            {endpoints.data.map((e) => (
              <tr key={e.id}>
                <Td>
                  <TextLink to={`/webhooks/${e.id}`} className="font-mono text-xs">
                    {e.url}
                  </TextLink>
                  {e.description && <span className="block text-muted">{e.description}</span>}
                </Td>
                <Td className="font-mono text-xs">{e.event_types.length === 0 ? "all" : e.event_types.join(", ")}</Td>
                <Td>{e.enabled ? "enabled" : <span className="text-muted">disabled</span>}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      <Pagination nextCursor={endpoints.next_cursor} />
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
