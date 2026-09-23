import { ActionError } from "~/components/confirm-action";
import { DeliveriesTable } from "~/components/deliveries-table";
import { Details } from "~/components/details";
import { JsonView } from "~/components/json-view";
import { PageHeader, Section } from "~/components/page";
import { IdLink } from "~/components/refs";
import { Notice } from "~/components/ui/notice";
import { api, fetchAll, path } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { retryDelivery } from "~/lib/deliveries";
import { formatDateTime } from "~/lib/format";
import { routeId } from "~/lib/ids";
import { title } from "~/lib/meta";
import { withQuery } from "~/lib/query";
import { canWrite } from "~/lib/session";
import type { Event, WebhookDelivery } from "~/lib/types";
import type { Route } from "./+types/detail";

export const meta: Route.MetaFunction = ({ loaderData }) => title(loaderData?.event.type ?? "Event");

export async function clientLoader({ params, request }: Route.ClientLoaderArgs) {
  const id = routeId(params.eventId, "evt");
  const [event, deliveries] = await Promise.all([
    api<Event>(path`/v1/events/${id}`, { signal: request.signal }),
    fetchAll<WebhookDelivery>(withQuery("/v1/webhook_deliveries", { event_id: id }), { signal: request.signal, max: 100 }),
  ]);
  return { event, deliveries };
}

export async function clientAction({ request }: Route.ClientActionArgs) {
  return retryDelivery(await request.formData(), request.signal);
}

export default function EventDetail({ loaderData }: Route.ComponentProps) {
  const { event, deliveries } = loaderData;
  const about = typeof event.data?.id === "string" ? event.data.id : null;
  return (
    <div className="grid gap-8">
      <PageHeader title={event.type} />
      <Details
        items={[
          { term: "ID", value: event.id, mono: true },
          { term: "Created", value: formatDateTime(event.created_at) },
          ...(about ? [{ term: "About", value: <IdLink id={about} /> }] : []),
        ]}
      />
      <Section title="Data">
        <p className="text-muted">The resource as it was when the change committed. Webhooks receive this event as their body.</p>
        <JsonView value={event} />
      </Section>
      <Section title="Webhook deliveries">
        <ActionError fetcherKey="delivery-retry" />
        {deliveries.length === 0 ? <Notice>No endpoint subscribes to this event.</Notice> : <DeliveriesTable deliveries={deliveries} canRetry={canWrite(useApiKey().role)} />}
      </Section>
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
