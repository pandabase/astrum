import { useState } from "react";
import { redirect } from "react-router";
import { AccountSelect } from "~/components/account-select";
import { LedgerPicker } from "~/components/ledger-picker";
import { PageHeader } from "~/components/page";
import { ResourceForm } from "~/components/resource-form";
import { Field, Input } from "~/components/ui/field";
import { accountsById, ledgerAccounts } from "~/lib/accounts";
import { api, fetchAll, path } from "~/lib/api";
import { parseAmount } from "~/lib/format";
import { formError, text } from "~/lib/forms";
import { isId } from "~/lib/ids";
import { useIdempotencyKey } from "~/lib/idempotency";
import { title } from "~/lib/meta";
import { searchParams } from "~/lib/query";
import { toApiTime, toLocalInput } from "~/lib/time";
import type { Hold, Ledger } from "~/lib/types";
import type { Route } from "./+types/new";

export const meta: Route.MetaFunction = () => title("New hold");

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
  const accountId = text(form, "account_id");
  const account = (await accountsById([accountId], request.signal)).get(accountId);
  if (!account) return { error: "Choose an account." };
  const amount = parseAmount(text(form, "amount"), account.currency_exponent);
  if (amount === null || amount.startsWith("-") || amount === "0") {
    return { error: `Enter a positive amount with at most ${account.currency_exponent} decimal places.` };
  }
  const expiresAt = toApiTime(text(form, "expires_at"));
  if (!expiresAt) return { error: "Choose when the hold expires." };
  let hold: Hold;
  try {
    hold = await api<Hold>("/v1/holds", {
      method: "POST",
      body: { account_id: accountId, amount, description: text(form, "description"), expires_at: expiresAt },
      idempotencyKey: text(form, "idempotency_key"),
      signal: request.signal,
    });
  } catch (err) {
    return formError(err);
  }
  throw redirect(`/holds/${hold.id}`);
}

export default function NewHold({ loaderData, actionData }: Route.ComponentProps) {
  const idempotencyKey = useIdempotencyKey(actionData);
  const [defaultExpiry] = useState(() => toLocalInput(new Date(Date.now() + 7 * 24 * 3600 * 1000).toISOString()));
  const { ledger, accounts, ledgers, accountId } = loaderData;
  return (
    <div className="grid gap-6">
      <PageHeader title={ledger ? `New hold in ${ledger.name}` : "New hold"} />
      {!ledger ? (
        <LedgerPicker ledgers={ledgers} />
      ) : (
        <ResourceForm error={actionData?.error} submitLabel="Create hold" cancelTo="/holds" idempotencyKey={idempotencyKey}>
          <Field label="Account">{(props) => <AccountSelect {...props} name="account_id" accounts={accounts} defaultValue={accountId} required />}</Field>
          <Field label="Amount" hint="In the account's currency, such as 12.50.">
            {(props) => <Input {...props} name="amount" inputMode="decimal" required className="w-48 text-right tabular-nums" />}
          </Field>
          <Field label="Description">{(props) => <Input {...props} name="description" maxLength={1000} />}</Field>
          <Field label="Expires" hint="Released automatically at this time if not captured or voided, in your time zone.">
            {(props) => <Input {...props} name="expires_at" type="datetime-local" defaultValue={defaultExpiry} required className="w-64" />}
          </Field>
        </ResourceForm>
      )}
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
