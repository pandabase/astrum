import { Amount } from "~/components/amount";
import { ActionError, ConfirmAction } from "~/components/confirm-action";
import { Details, MetadataValue } from "~/components/details";
import { PageHeader, Section } from "~/components/page";
import { AccountRef, IdLink } from "~/components/refs";
import { StatusText } from "~/components/status";
import { Notice } from "~/components/ui/notice";
import { Table, Td, Th } from "~/components/ui/table";
import { accountsById } from "~/lib/accounts";
import { api, path } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { formatDateTime } from "~/lib/format";
import { formError } from "~/lib/forms";
import { routeId } from "~/lib/ids";
import { title } from "~/lib/meta";
import { canWrite } from "~/lib/session";
import type { ScheduledTransaction } from "~/lib/types";
import type { Route } from "./+types/detail";

export const meta: Route.MetaFunction = () => title("Scheduled transaction");

export async function clientLoader({ params, request }: Route.ClientLoaderArgs) {
  const id = routeId(params.scheduleId, "sched");
  const schedule = await api<ScheduledTransaction>(path`/v1/scheduled_transactions/${id}`, { signal: request.signal });
  const accounts = await accountsById(schedule.entries.map((e) => e.account_id), request.signal);
  return { schedule, accounts };
}

const fetcherKey = "schedule-action";

export async function clientAction({ params, request }: Route.ClientActionArgs) {
  const id = routeId(params.scheduleId, "sched");
  try {
    await api(path`/v1/scheduled_transactions/${id}/cancel`, { method: "POST", signal: request.signal });
  } catch (err) {
    return formError(err);
  }
  return null;
}

export default function ScheduleDetail({ loaderData }: Route.ComponentProps) {
  const { schedule: s, accounts } = loaderData;
  return (
    <div className="grid gap-8">
      <PageHeader
        title={s.description || "Scheduled transaction"}
        actions={
          canWrite(useApiKey().role) &&
          s.status === "scheduled" && (
            <ConfirmAction fetcherKey={fetcherKey} intent="cancel" label="Cancel schedule" title="Cancel this scheduled transaction?" confirmLabel="Cancel schedule" variant="danger">
              <p>The transaction will not be posted. This cannot be undone.</p>
            </ConfirmAction>
          )
        }
      />
      <ActionError fetcherKey={fetcherKey} />
      {s.failure && <Notice tone="danger">It could not be posted: {s.failure}</Notice>}
      <Section title="Entries">
        <Table>
          <thead>
            <tr>
              <Th>Account</Th>
              <Th numeric>Debit</Th>
              <Th numeric>Credit</Th>
            </tr>
          </thead>
          <tbody>
            {s.entries.map((entry, i) => {
              const exponent = accounts.get(entry.account_id)?.currency_exponent ?? 0;
              return (
                <tr key={i}>
                  <Td>
                    <AccountRef id={entry.account_id} accounts={accounts} />
                  </Td>
                  <Td numeric>{entry.side === "debit" && <Amount value={entry.amount} exponent={exponent} />}</Td>
                  <Td numeric>{entry.side === "credit" && <Amount value={entry.amount} exponent={exponent} />}</Td>
                </tr>
              );
            })}
          </tbody>
        </Table>
      </Section>
      <Details
        items={[
          { term: "Status", value: <StatusText status={s.status} /> },
          { term: "Posts at", value: formatDateTime(s.execute_at) },
          ...(s.transaction_id ? [{ term: "Transaction", value: <IdLink id={s.transaction_id} /> }] : []),
          { term: "Created", value: formatDateTime(s.created_at) },
          ...(s.resolved_at ? [{ term: "Resolved", value: formatDateTime(s.resolved_at) }] : []),
          { term: "ID", value: s.id, mono: true },
          { term: "Idempotency key", value: s.idempotency_key, mono: true },
          { term: "Metadata", value: <MetadataValue metadata={s.metadata} /> },
        ]}
      />
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
