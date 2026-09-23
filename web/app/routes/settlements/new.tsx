import { redirect } from "react-router";
import { AccountSelect } from "~/components/account-select";
import { LedgerPicker } from "~/components/ledger-picker";
import { PageHeader } from "~/components/page";
import { ResourceForm } from "~/components/resource-form";
import { Field, Input } from "~/components/ui/field";
import { ledgerAccounts } from "~/lib/accounts";
import { api, fetchAll, path } from "~/lib/api";
import { formError, text } from "~/lib/forms";
import { isId } from "~/lib/ids";
import { useIdempotencyKey } from "~/lib/idempotency";
import { title } from "~/lib/meta";
import { searchParams } from "~/lib/query";
import { toApiTime } from "~/lib/time";
import type { Ledger, Settlement } from "~/lib/types";
import type { Route } from "./+types/new";

export const meta: Route.MetaFunction = () => title("New settlement");

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const { filters } = searchParams(request, ["ledger_id", "account_id"]);
  if (!isId(filters.ledger_id, "ldg")) {
    return { ledger: null, accounts: [], ledgers: await fetchAll<Ledger>("/v1/ledgers", { signal: request.signal }), accountId: "" };
  }
  const [ledger, accounts] = await Promise.all([
    api<Ledger>(path`/v1/ledgers/${filters.ledger_id}`, { signal: request.signal }),
    ledgerAccounts(filters.ledger_id, request.signal),
  ]);
  return { ledger, accounts, ledgers: [], accountId: filters.account_id };
}

export async function clientAction({ request }: Route.ClientActionArgs) {
  const form = await request.formData();
  const boundText = text(form, "until");
  const bound = toApiTime(boundText);
  if (boundText && !bound) return { error: "The cut-off is not a valid date." };
  let settlement: Settlement;
  try {
    settlement = await api<Settlement>("/v1/settlements", {
      method: "POST",
      body: {
        settled_account_id: text(form, "settled_account_id"),
        contra_account_id: text(form, "contra_account_id"),
        description: text(form, "description"),
        ...(bound ? { effective_at_upper_bound: bound } : {}),
      },
      idempotencyKey: text(form, "idempotency_key"),
      signal: request.signal,
    });
  } catch (err) {
    return formError(err);
  }
  throw redirect(`/settlements/${settlement.id}`);
}

export default function NewSettlement({ loaderData, actionData }: Route.ComponentProps) {
  const idempotencyKey = useIdempotencyKey(actionData);
  const { ledger, accounts, ledgers, accountId } = loaderData;
  return (
    <div className="grid gap-6">
      <PageHeader title="New settlement" />
      {!ledger ? (
        <LedgerPicker ledgers={ledgers} />
      ) : (
        <ResourceForm error={actionData?.error} submitLabel="Settle" cancelTo="/settlements" idempotencyKey={idempotencyKey}>
          <Field label="Account to settle" hint="Everything on it that has not been settled yet is moved out.">
            {(props) => <AccountSelect {...props} name="settled_account_id" accounts={accounts} defaultValue={accountId} required />}
          </Field>
          <Field label="Contra account" hint="Receives the net amount, such as a bank or payouts account in the same currency.">
            {(props) => <AccountSelect {...props} name="contra_account_id" accounts={accounts} required />}
          </Field>
          <Field label="Cut-off" hint="Only settle entries effective before this time. Leave blank to settle everything.">
            {(props) => <Input {...props} name="until" type="datetime-local" className="w-64" />}
          </Field>
          <Field label="Description">{(props) => <Input {...props} name="description" maxLength={1000} />}</Field>
        </ResourceForm>
      )}
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
