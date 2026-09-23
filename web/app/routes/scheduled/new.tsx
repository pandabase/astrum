import { redirect } from "react-router";
import { LedgerPicker } from "~/components/ledger-picker";
import { PageHeader } from "~/components/page";
import { TransactionForm } from "~/components/transaction-form";
import { accountsById, ledgerAccounts } from "~/lib/accounts";
import { api, fetchAll, path } from "~/lib/api";
import { formError, text } from "~/lib/forms";
import { isId } from "~/lib/ids";
import { useIdempotencyKey } from "~/lib/idempotency";
import { title } from "~/lib/meta";
import { searchParams } from "~/lib/query";
import { toApiTime } from "~/lib/time";
import { entriesFromDrafts, readDrafts, readTransactionFields, transactionBody } from "~/lib/transaction-form";
import type { Ledger, ScheduledTransaction } from "~/lib/types";
import type { Route } from "./+types/new";

export const meta: Route.MetaFunction = () => title("Schedule transaction");

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const ledgerId = searchParams(request, ["ledger_id"]).filters.ledger_id;
  if (!isId(ledgerId, "ldg")) {
    return { ledger: null, accounts: [], ledgers: await fetchAll<Ledger>("/v1/ledgers", { signal: request.signal }) };
  }
  const [ledger, accounts] = await Promise.all([
    api<Ledger>(path`/v1/ledgers/${ledgerId}`, { signal: request.signal }),
    ledgerAccounts(ledgerId, request.signal),
  ]);
  return { ledger, accounts, ledgers: [] };
}

export async function clientAction({ request }: Route.ClientActionArgs) {
  const form = await request.formData();
  const executeAt = toApiTime(text(form, "execute_at"));
  if (!executeAt) return { error: "Choose when to post the transaction." };
  const fields = readTransactionFields(form);
  if (!fields.ok) return fields;
  const drafts = readDrafts(form);
  const entries = entriesFromDrafts(drafts, await accountsById(drafts.map((d) => d.accountId), request.signal));
  if (!entries.ok) return entries;
  let schedule: ScheduledTransaction;
  try {
    schedule = await api<ScheduledTransaction>("/v1/scheduled_transactions", {
      method: "POST",
      body: { ...transactionBody(fields.value, entries.value), execute_at: executeAt },
      idempotencyKey: text(form, "idempotency_key"),
      signal: request.signal,
    });
  } catch (err) {
    return formError(err);
  }
  throw redirect(`/scheduled/${schedule.id}`);
}

export default function NewSchedule({ loaderData, actionData }: Route.ComponentProps) {
  const idempotencyKey = useIdempotencyKey(actionData);
  const { ledger, accounts, ledgers } = loaderData;
  return (
    <div className="grid gap-6">
      <PageHeader title={ledger ? `Schedule a transaction in ${ledger.name}` : "Schedule a transaction"} />
      {ledger ? (
        <TransactionForm
          mode="schedule"
          accounts={accounts}
          error={actionData?.error}
          submitLabel="Schedule"
          cancelTo="/scheduled"
          idempotencyKey={idempotencyKey}
        />
      ) : (
        <LedgerPicker ledgers={ledgers} />
      )}
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
