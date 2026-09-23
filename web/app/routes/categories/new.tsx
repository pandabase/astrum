import { redirect } from "react-router";
import { LedgerPicker } from "~/components/ledger-picker";
import { NormalSideField } from "~/components/normal-side-field";
import { PageHeader } from "~/components/page";
import { DescriptiveFields, ResourceForm } from "~/components/resource-form";
import { Field, Input } from "~/components/ui/field";
import { api, fetchAll, path } from "~/lib/api";
import { formError, readDescriptive, text } from "~/lib/forms";
import { isId } from "~/lib/ids";
import { useIdempotencyKey } from "~/lib/idempotency";
import { title } from "~/lib/meta";
import { searchParams } from "~/lib/query";
import type { Category, Ledger } from "~/lib/types";
import type { Route } from "./+types/new";

export const meta: Route.MetaFunction = () => title("New category");

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const ledgerId = searchParams(request, ["ledger_id"]).filters.ledger_id;
  if (!isId(ledgerId, "ldg")) return { ledger: null, ledgers: await fetchAll<Ledger>("/v1/ledgers", { signal: request.signal }) };
  return { ledger: await api<Ledger>(path`/v1/ledgers/${ledgerId}`, { signal: request.signal }), ledgers: [] };
}

export async function clientAction({ request }: Route.ClientActionArgs) {
  const ledgerId = searchParams(request, ["ledger_id"]).filters.ledger_id;
  const form = await request.formData();
  const details = readDescriptive(form);
  if (!details.ok) return details;
  let category: Category;
  try {
    category = await api<Category>("/v1/account_categories", {
      method: "POST",
      body: { ledger_id: ledgerId, currency: text(form, "currency").toUpperCase(), normal_side: text(form, "normal_side"), ...details.value },
      idempotencyKey: text(form, "idempotency_key"),
      signal: request.signal,
    });
  } catch (err) {
    return formError(err);
  }
  throw redirect(`/categories/${category.id}`);
}

export default function NewCategory({ loaderData, actionData }: Route.ComponentProps) {
  const idempotencyKey = useIdempotencyKey(actionData);
  const { ledger, ledgers } = loaderData;
  return (
    <div className="grid gap-6">
      <PageHeader title={ledger ? `New category in ${ledger.name}` : "New category"} />
      {!ledger ? (
        <LedgerPicker ledgers={ledgers} />
      ) : (
        <ResourceForm error={actionData?.error} submitLabel="Create category" cancelTo="/categories" idempotencyKey={idempotencyKey}>
          <Field label="Currency" hint="Every account and nested category must use this currency.">
            {(props) => <Input {...props} name="currency" defaultValue="USD" required maxLength={16} className="w-32 uppercase" />}
          </Field>
          <NormalSideField debitHint="such as Assets or Expenses." creditHint="such as Liabilities or Revenue." />
          <DescriptiveFields nameRequired />
        </ResourceForm>
      )}
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
