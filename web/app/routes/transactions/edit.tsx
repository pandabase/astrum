import { redirect } from "react-router";
import { PageHeader } from "~/components/page";
import { TransactionForm } from "~/components/transaction-form";
import { Notice } from "~/components/ui/notice";
import { accountsById, ledgerAccounts } from "~/lib/accounts";
import { api, path } from "~/lib/api";
import { formError } from "~/lib/forms";
import { routeId } from "~/lib/ids";
import { mergePatch } from "~/lib/metadata";
import { title } from "~/lib/meta";
import { draftsFromEntries, entriesFromDrafts, readDrafts, readTransactionFields } from "~/lib/transaction-form";
import type { Transaction } from "~/lib/types";
import type { Route } from "./+types/edit";

export const meta: Route.MetaFunction = () => title("Edit transaction");

export async function clientLoader({ params, request }: Route.ClientLoaderArgs) {
  const id = routeId(params.transactionId, "txn");
  const transaction = await api<Transaction>(path`/v1/transactions/${id}`, { signal: request.signal });
  const accounts = await ledgerAccounts(transaction.ledger_id, request.signal);
  return { transaction, accounts };
}

export async function clientAction({ params, request }: Route.ClientActionArgs) {
  const id = routeId(params.transactionId, "txn");
  const form = await request.formData();
  const fields = readTransactionFields(form);
  if (!fields.ok) return fields;
  const drafts = readDrafts(form);
  const entries = entriesFromDrafts(drafts, await accountsById(drafts.map((d) => d.accountId), request.signal));
  if (!entries.ok) return entries;
  try {
    await api<Transaction>(path`/v1/transactions/${id}`, {
      method: "PATCH",
      body: {
        description: fields.value.description,
        metadata: mergePatch(JSON.parse(String(form.get("original_metadata") || "{}")), fields.value.metadata),
        entries: entries.value,
        ...(fields.value.effectiveAt ? { effective_at: fields.value.effectiveAt } : {}),
      },
      signal: request.signal,
    });
  } catch (err) {
    return formError(err);
  }
  throw redirect(`/transactions/${id}`);
}

export default function EditTransaction({ loaderData, actionData }: Route.ComponentProps) {
  const { transaction, accounts } = loaderData;
  const byId = new Map(accounts.map((a) => [a.id, a]));
  return (
    <div className="grid gap-6">
      <PageHeader title="Edit pending transaction" />
      {transaction.status !== "pending" ? (
        <Notice tone="warning">Only pending transactions can be edited. This one is {transaction.status}.</Notice>
      ) : (
        <>
          <p className="max-w-2xl text-muted">Saving creates version {transaction.version + 1}. Earlier versions are kept.</p>
          <TransactionForm
            mode="edit"
            accounts={accounts}
            error={actionData?.error}
            submitLabel="Save changes"
            cancelTo={`/transactions/${transaction.id}`}
            defaults={{
              entries: draftsFromEntries(transaction.entries, byId),
              description: transaction.description,
              metadata: transaction.metadata,
              effectiveAt: transaction.effective_at,
            }}
          />
        </>
      )}
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
