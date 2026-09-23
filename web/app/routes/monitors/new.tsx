import { redirect } from "react-router";
import { AccountSelect } from "~/components/account-select";
import { LedgerPicker } from "~/components/ledger-picker";
import { PageHeader } from "~/components/page";
import { ResourceForm } from "~/components/resource-form";
import { Field, Input, Select, Textarea } from "~/components/ui/field";
import { accountsById, ledgerAccounts } from "~/lib/accounts";
import { api, fetchAll, path } from "~/lib/api";
import { parseAmount } from "~/lib/format";
import { formError, text } from "~/lib/forms";
import { isId } from "~/lib/ids";
import { parseMetadata } from "~/lib/metadata";
import { title } from "~/lib/meta";
import { monitorFields, monitorOperators } from "~/lib/monitors";
import { searchParams } from "~/lib/query";
import type { BalanceMonitor, Ledger } from "~/lib/types";
import type { Route } from "./+types/new";

export const meta: Route.MetaFunction = () => title("New balance monitor");

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
  const value = parseAmount(text(form, "value"), account.currency_exponent);
  if (value === null) return { error: `Enter an amount with at most ${account.currency_exponent} decimal places.` };
  const metadata = parseMetadata(text(form, "metadata"));
  if (!metadata.ok) return metadata;
  let monitor: BalanceMonitor;
  try {
    monitor = await api<BalanceMonitor>("/v1/balance_monitors", {
      method: "POST",
      body: {
        account_id: accountId,
        alert_condition: { field: text(form, "field"), operator: text(form, "operator"), value },
        description: text(form, "description"),
        metadata: metadata.value,
      },
      signal: request.signal,
    });
  } catch (err) {
    return formError(err);
  }
  throw redirect(`/monitors/${monitor.id}`);
}

export default function NewMonitor({ loaderData, actionData }: Route.ComponentProps) {
  const { ledger, accounts, ledgers, accountId } = loaderData;
  return (
    <div className="grid gap-6">
      <PageHeader title="New balance monitor" />
      {!ledger ? (
        <LedgerPicker ledgers={ledgers} />
      ) : (
        <ResourceForm error={actionData?.error} submitLabel="Create monitor" cancelTo="/monitors">
          <Field label="Account">{(props) => <AccountSelect {...props} name="account_id" accounts={accounts} allowInactive defaultValue={accountId} required />}</Field>
          <fieldset className="grid gap-1">
            <legend className="mb-1 font-medium">Condition</legend>
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-muted">Alert when the</span>
              <Select name="field" aria-label="Balance" className="w-36">
                {Object.entries(monitorFields).map(([value, label]) => (
                  <option key={value} value={value}>
                    {label}
                  </option>
                ))}
              </Select>
              <span className="text-muted">balance is</span>
              <Select name="operator" aria-label="Operator" defaultValue="lt" className="w-16">
                {Object.entries(monitorOperators).map(([value, label]) => (
                  <option key={value} value={value}>
                    {label}
                  </option>
                ))}
              </Select>
              <Input name="value" aria-label="Amount" inputMode="decimal" required className="w-40 text-right tabular-nums" />
            </div>
          </fieldset>
          <Field label="Description">{(props) => <Input {...props} name="description" maxLength={1000} />}</Field>
          <Field label="Metadata" hint="A JSON object. Leave blank for none.">
            {(props) => <Textarea {...props} name="metadata" spellCheck={false} className="min-h-16 font-mono text-xs" />}
          </Field>
        </ResourceForm>
      )}
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
