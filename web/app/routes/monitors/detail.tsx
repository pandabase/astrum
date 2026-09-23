import { redirect, useFetcher } from "react-router";
import { Amount } from "~/components/amount";
import { ActionError, ConfirmAction } from "~/components/confirm-action";
import { Details, MetadataValue } from "~/components/details";
import { PageHeader, Section } from "~/components/page";
import { AccountRef } from "~/components/refs";
import { Button } from "~/components/ui/button";
import { Field, Input, Textarea } from "~/components/ui/field";
import { TextLink } from "~/components/ui/link";
import { accountsById } from "~/lib/accounts";
import { api, path } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { formatDateTime } from "~/lib/format";
import { formError, text } from "~/lib/forms";
import { routeId } from "~/lib/ids";
import { formatMetadata, mergePatch, parseMetadata } from "~/lib/metadata";
import { title } from "~/lib/meta";
import { monitorFields, monitorOperators } from "~/lib/monitors";
import { canWrite } from "~/lib/session";
import type { BalanceMonitor } from "~/lib/types";
import type { Route } from "./+types/detail";

export const meta: Route.MetaFunction = () => title("Balance monitor");

export async function clientLoader({ params, request }: Route.ClientLoaderArgs) {
  const id = routeId(params.monitorId, "bm");
  const monitor = await api<BalanceMonitor>(path`/v1/balance_monitors/${id}`, { signal: request.signal });
  const accounts = await accountsById([monitor.account_id], request.signal);
  return { monitor, accounts };
}

const fetcherKey = "monitor-action";

export async function clientAction({ params, request }: Route.ClientActionArgs) {
  const id = routeId(params.monitorId, "bm");
  const form = await request.formData();
  try {
    if (text(form, "intent") === "delete") {
      await api(path`/v1/balance_monitors/${id}`, { method: "DELETE", signal: request.signal });
      throw redirect("/monitors");
    }
    const metadata = parseMetadata(text(form, "metadata"));
    if (!metadata.ok) return metadata;
    const original = parseMetadata(text(form, "original_metadata"));
    await api(path`/v1/balance_monitors/${id}`, {
      method: "PATCH",
      body: { description: text(form, "description"), metadata: mergePatch(original.ok ? original.value : {}, metadata.value) },
      signal: request.signal,
    });
    return null;
  } catch (err) {
    if (err instanceof Response) throw err;
    return formError(err);
  }
}

export default function MonitorDetail({ loaderData }: Route.ComponentProps) {
  const { monitor: m, accounts } = loaderData;
  const account = accounts.get(m.account_id);
  const writable = canWrite(useApiKey().role);
  const fetcher = useFetcher({ key: fetcherKey });
  return (
    <div className="grid gap-8">
      <PageHeader
        title={m.description || "Balance monitor"}
        actions={
          writable && (
            <ConfirmAction fetcherKey={fetcherKey} intent="delete" label="Delete" title="Delete this monitor?" confirmLabel="Delete monitor" variant="danger">
              <p>No more alerts will be sent for this condition.</p>
            </ConfirmAction>
          )
        }
      />
      <ActionError fetcherKey={fetcherKey} />
      <Details
        items={[
          { term: "Account", value: <AccountRef id={m.account_id} accounts={accounts} /> },
          {
            term: "Condition",
            value: (
              <>
                {monitorFields[m.alert_condition.field]} balance {monitorOperators[m.alert_condition.operator]}{" "}
                <Amount value={m.alert_condition.value} exponent={account?.currency_exponent ?? 0} currency={account?.currency} />
              </>
            ),
          },
          { term: "Now", value: m.triggered ? <span className="text-warning">In condition</span> : "Not in condition" },
          { term: "Alerts", value: <TextLink to="/events?type=balance_monitor.triggered">balance_monitor.triggered events</TextLink> },
          { term: "Created", value: formatDateTime(m.created_at) },
          { term: "ID", value: m.id, mono: true },
          { term: "Metadata", value: <MetadataValue metadata={m.metadata} /> },
        ]}
      />
      {writable && (
        <Section title="Edit">
          <p className="text-muted">The account and condition cannot change; create a new monitor instead.</p>
          <fetcher.Form method="post" className="grid max-w-xl gap-4">
            <input type="hidden" name="intent" value="update" />
            <input type="hidden" name="original_metadata" value={JSON.stringify(m.metadata ?? {})} />
            <Field label="Description">{(props) => <Input {...props} name="description" defaultValue={m.description} />}</Field>
            <Field label="Metadata">
              {(props) => <Textarea {...props} name="metadata" defaultValue={formatMetadata(m.metadata)} spellCheck={false} className="min-h-16 font-mono text-xs" />}
            </Field>
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
