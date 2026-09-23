import { redirect } from "react-router";
import { PageHeader } from "~/components/page";
import { DescriptiveFields, ResourceForm } from "~/components/resource-form";
import { api } from "~/lib/api";
import { formError, readDescriptive, text } from "~/lib/forms";
import { useIdempotencyKey } from "~/lib/idempotency";
import { title } from "~/lib/meta";
import type { Ledger } from "~/lib/types";
import type { Route } from "./+types/new";

export const meta: Route.MetaFunction = () => title("New ledger");

export async function clientAction({ request }: Route.ClientActionArgs) {
  const form = await request.formData();
  const details = readDescriptive(form);
  if (!details.ok) return details;
  let ledger: Ledger;
  try {
    ledger = await api<Ledger>("/v1/ledgers", {
      method: "POST",
      body: details.value,
      idempotencyKey: text(form, "idempotency_key"),
      signal: request.signal,
    });
  } catch (err) {
    return formError(err);
  }
  throw redirect(`/ledgers/${ledger.id}`);
}

export default function NewLedger({ actionData }: Route.ComponentProps) {
  const idempotencyKey = useIdempotencyKey(actionData);
  return (
    <div className="grid gap-6">
      <PageHeader title="New ledger" />
      <ResourceForm error={actionData?.error} submitLabel="Create ledger" cancelTo="/ledgers" idempotencyKey={idempotencyKey}>
        <DescriptiveFields nameRequired />
      </ResourceForm>
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
