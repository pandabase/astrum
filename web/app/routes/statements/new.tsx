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
import { title } from "~/lib/meta";
import { searchParams } from "~/lib/query";
import { toApiTime } from "~/lib/time";
import type { Ledger, Statement } from "~/lib/types";
import type { Route } from "./+types/new";

export const meta: Route.MetaFunction = () => title("New statement");

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
  const from = toApiTime(text(form, "from"));
  const until = toApiTime(text(form, "until"));
  if (!from || !until) return { error: "Choose the start and end of the period." };
  let statement: Statement;
  try {
    statement = await api<Statement>("/v1/statements", {
      method: "POST",
      body: { account_id: text(form, "account_id"), description: text(form, "description"), effective_at_lower_bound: from, effective_at_upper_bound: until },
      signal: request.signal,
    });
  } catch (err) {
    return formError(err);
  }
  throw redirect(`/statements/${statement.id}`);
}

export default function NewStatement({ loaderData, actionData }: Route.ComponentProps) {
  const { ledger, accounts, ledgers, accountId } = loaderData;
  return (
    <div className="grid gap-6">
      <PageHeader title="New statement" />
      {!ledger ? (
        <LedgerPicker ledgers={ledgers} />
      ) : (
        <ResourceForm error={actionData?.error} submitLabel="Create statement" cancelTo="/statements">
          <Field label="Account">{(props) => <AccountSelect {...props} name="account_id" accounts={accounts} allowInactive defaultValue={accountId} required />}</Field>
          <Field label="From" hint="Included. In your time zone.">
            {(props) => <Input {...props} name="from" type="datetime-local" required className="w-64" />}
          </Field>
          <Field label="Until" hint="Not included, so the next statement can start here.">
            {(props) => <Input {...props} name="until" type="datetime-local" required className="w-64" />}
          </Field>
          <Field label="Description">{(props) => <Input {...props} name="description" maxLength={1000} />}</Field>
        </ResourceForm>
      )}
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
