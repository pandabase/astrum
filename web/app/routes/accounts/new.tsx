import { redirect } from "react-router";
import { NormalSideField } from "~/components/normal-side-field";
import { PageHeader } from "~/components/page";
import { DescriptiveFields, ResourceForm } from "~/components/resource-form";
import { Checkbox, Field, Input } from "~/components/ui/field";
import { ApiError, api, path } from "~/lib/api";
import { parseAmount } from "~/lib/format";
import { formError, readDescriptive, text } from "~/lib/forms";
import { routeId } from "~/lib/ids";
import { useIdempotencyKey } from "~/lib/idempotency";
import { title } from "~/lib/meta";
import type { Account, Currency, Ledger } from "~/lib/types";
import type { Route } from "./+types/new";

export const meta: Route.MetaFunction = () => title("New account");

export async function clientLoader({ params, request }: Route.ClientLoaderArgs) {
  const ledgerId = routeId(params.ledgerId, "ldg");
  return api<Ledger>(path`/v1/ledgers/${ledgerId}`, { signal: request.signal });
}

export async function clientAction({ params, request }: Route.ClientActionArgs) {
  const ledgerId = routeId(params.ledgerId, "ldg");
  const form = await request.formData();
  const details = readDescriptive(form);
  if (!details.ok) return details;
  const currencyCode = text(form, "currency").toUpperCase();
  const normalSide = text(form, "normal_side");
  if (normalSide !== "debit" && normalSide !== "credit") return { error: "Choose a normal side." };

  let account: Account;
  try {
    // The overdraft limit is typed in the currency's units, so its exponent is needed to convert it.
    let overdraftLimit = "0";
    const overdraftText = text(form, "overdraft_limit");
    if (overdraftText !== "") {
      const currency = await api<Currency>(path`/v1/currencies/${currencyCode}`, { signal: request.signal }).catch((err) => {
        if (err instanceof ApiError && err.status === 404) return null;
        throw err;
      });
      if (!currency) return { error: `${currencyCode} is not a registered currency.` };
      const parsed = parseAmount(overdraftText, currency.exponent);
      if (parsed === null || parsed.startsWith("-")) {
        return { error: `Overdraft limit must be a positive amount with at most ${currency.exponent} decimal places.` };
      }
      overdraftLimit = parsed;
    }

    account = await api<Account>("/v1/accounts", {
      method: "POST",
      body: {
        ledger_id: ledgerId,
        code: text(form, "code"),
        currency: currencyCode,
        normal_side: normalSide,
        allow_negative: form.get("allow_negative") === "on",
        overdraft_limit: overdraftLimit,
        ...details.value,
      },
      idempotencyKey: text(form, "idempotency_key"),
      signal: request.signal,
    });
  } catch (err) {
    return formError(err);
  }
  throw redirect(`/accounts/${account.id}`);
}

export default function NewAccount({ loaderData: ledger, actionData }: Route.ComponentProps) {
  const idempotencyKey = useIdempotencyKey(actionData);
  return (
    <div className="grid gap-6">
      <PageHeader title={`New account in ${ledger.name}`} />
      <ResourceForm
        error={actionData?.error}
        submitLabel="Create account"
        cancelTo={`/ledgers/${ledger.id}`}
        idempotencyKey={idempotencyKey}
      >
        <Field label="Code" hint="Your unique reference for the account in this ledger, such as wallet:alice. It cannot change.">
          {(props) => <Input {...props} name="code" required maxLength={128} spellCheck={false} className="font-mono" />}
        </Field>
        <Field label="Currency" hint="A registered currency code. It cannot change.">
          {(props) => <Input {...props} name="currency" required defaultValue="USD" maxLength={16} className="w-32 uppercase" />}
        </Field>
        <NormalSideField
          debitHint="grows with debits: assets and expenses, such as cash or receivables."
          creditHint="grows with credits: liabilities, equity and revenue, such as user balances."
        />
        <DescriptiveFields />
        <Checkbox name="allow_negative" label="Allow negative balance" hint="Lets the balance go below zero without limit." />
        <Field label="Overdraft limit" hint="How far below zero the balance may go when negative balances are not allowed. Leave blank for none.">
          {(props) => <Input {...props} name="overdraft_limit" inputMode="decimal" className="w-48 text-right tabular-nums" />}
        </Field>
      </ResourceForm>
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
