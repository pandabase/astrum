import { redirect, useFetcher } from "react-router";
import { ActionError, ConfirmAction } from "~/components/confirm-action";
import { DeliveriesTable } from "~/components/deliveries-table";
import { Details } from "~/components/details";
import { EventTypeFields } from "~/components/event-type-fields";
import { FilterBar } from "~/components/filter-bar";
import { PageHeader, Section } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { Button } from "~/components/ui/button";
import { Checkbox, Field, Input, Select } from "~/components/ui/field";
import { Notice } from "~/components/ui/notice";
import { api, path } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { retryDelivery } from "~/lib/deliveries";
import { formatDateTime } from "~/lib/format";
import { formError, text } from "~/lib/forms";
import { routeId } from "~/lib/ids";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import { canWrite } from "~/lib/session";
import type { List, WebhookDelivery, WebhookEndpoint } from "~/lib/types";
import type { Route } from "./+types/detail";

export const meta: Route.MetaFunction = () => title("Webhook endpoint");

export async function clientLoader({ params, request }: Route.ClientLoaderArgs) {
  const id = routeId(params.endpointId, "we");
  const { cursor, filters } = searchParams(request, ["status"]);
  const [endpoint, deliveries] = await Promise.all([
    api<WebhookEndpoint>(path`/v1/webhook_endpoints/${id}`, { signal: request.signal }),
    api<List<WebhookDelivery>>(withQuery("/v1/webhook_deliveries", { endpoint_id: id, status: filters.status, cursor, limit: 50 }), {
      signal: request.signal,
    }),
  ]);
  return { endpoint, deliveries, filters };
}

const fetcherKey = "endpoint-action";

export async function clientAction({ params, request }: Route.ClientActionArgs) {
  const id = routeId(params.endpointId, "we");
  const form = await request.formData();
  const intent = text(form, "intent");
  if (intent === "retry") return retryDelivery(form, request.signal);
  try {
    if (intent === "delete") {
      await api(path`/v1/webhook_endpoints/${id}`, { method: "DELETE", signal: request.signal });
      throw redirect("/webhooks");
    }
    await api(path`/v1/webhook_endpoints/${id}`, {
      method: "PATCH",
      body: {
        url: text(form, "url"),
        description: text(form, "description"),
        event_types: form.getAll("event_types").map(String),
        enabled: form.get("enabled") === "on",
      },
      signal: request.signal,
    });
    return null;
  } catch (err) {
    if (err instanceof Response) throw err;
    return formError(err);
  }
}

export default function WebhookDetail({ loaderData }: Route.ComponentProps) {
  const { endpoint: e, deliveries, filters } = loaderData;
  const writable = canWrite(useApiKey().role);
  const fetcher = useFetcher({ key: fetcherKey });
  return (
    <div className="grid gap-8">
      <PageHeader
        title={e.description || "Webhook endpoint"}
        actions={
          writable && (
            <ConfirmAction fetcherKey={fetcherKey} intent="delete" label="Delete" title="Delete this endpoint?" confirmLabel="Delete endpoint" variant="danger">
              <p>Deliveries stop at once and the delivery log is deleted with it.</p>
            </ConfirmAction>
          )
        }
      />
      <ActionError fetcherKey={fetcherKey} />
      <Details
        items={[
          { term: "URL", value: e.url, mono: true },
          { term: "Status", value: e.enabled ? "Enabled" : <span className="text-muted">Disabled</span> },
          { term: "Events", value: e.event_types.length === 0 ? "All" : e.event_types.join(", "), mono: e.event_types.length > 0 },
          { term: "Created", value: formatDateTime(e.created_at) },
          { term: "ID", value: e.id, mono: true },
        ]}
      />

      <Section title="Deliveries">
        <ActionError fetcherKey="delivery-retry" />
        <FilterBar label="Filter deliveries" active={Boolean(filters.status)}>
          <Select name="status" defaultValue={filters.status} aria-label="Status" className="w-36">
            <option value="">Any status</option>
            <option value="pending">Pending</option>
            <option value="succeeded">Succeeded</option>
            <option value="failed">Failed</option>
          </Select>
        </FilterBar>
        {deliveries.data.length === 0 ? <Notice>No deliveries.</Notice> : <DeliveriesTable deliveries={deliveries.data} canRetry={writable} />}
        <Pagination nextCursor={deliveries.next_cursor} />
      </Section>

      {writable && (
        <Section title="Edit">
          <fetcher.Form method="post" className="grid max-w-3xl gap-4">
            <input type="hidden" name="intent" value="update" />
            <Field label="URL">{(props) => <Input {...props} name="url" type="url" required defaultValue={e.url} className="font-mono" />}</Field>
            <Field label="Description">{(props) => <Input {...props} name="description" defaultValue={e.description} />}</Field>
            <EventTypeFields selected={e.event_types} />
            <Checkbox name="enabled" defaultChecked={e.enabled} label="Enabled" hint="Disabled endpoints receive nothing until enabled again." />
            <div>
              <Button type="submit" disabled={fetcher.state !== "idle"}>
                Save changes
              </Button>
            </div>
          </fetcher.Form>
        </Section>
      )}
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
