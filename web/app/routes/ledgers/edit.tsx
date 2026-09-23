import { redirect } from "react-router";
import { PageHeader } from "~/components/page";
import { DescriptiveFields, ResourceForm } from "~/components/resource-form";
import { api, path } from "~/lib/api";
import { descriptivePatch, formError, readDescriptive, readOriginal } from "~/lib/forms";
import { routeId } from "~/lib/ids";
import { title } from "~/lib/meta";
import type { Ledger } from "~/lib/types";
import type { Route } from "./+types/edit";

export const meta: Route.MetaFunction = ({ loaderData }) => title(`Edit ${loaderData?.name ?? "ledger"}`);

export async function clientLoader({ params, request }: Route.ClientLoaderArgs) {
  const ledgerId = routeId(params.ledgerId, "ldg");
  return api<Ledger>(path`/v1/ledgers/${ledgerId}`, { signal: request.signal });
}

export async function clientAction({ params, request }: Route.ClientActionArgs) {
  const ledgerId = routeId(params.ledgerId, "ldg");
  const form = await request.formData();
  const details = readDescriptive(form);
  if (!details.ok) return details;
  const patch = descriptivePatch(readOriginal(form), details.value);
  try {
    if (Object.keys(patch).length > 0) {
      await api<Ledger>(path`/v1/ledgers/${ledgerId}`, { method: "PATCH", body: patch, signal: request.signal });
    }
  } catch (err) {
    return formError(err);
  }
  throw redirect(`/ledgers/${ledgerId}`);
}

export default function EditLedger({ loaderData: ledger, actionData }: Route.ComponentProps) {
  return (
    <div className="grid gap-6">
      <PageHeader title={`Edit ${ledger.name}`} />
      <ResourceForm error={actionData?.error} submitLabel="Save changes" cancelTo={`/ledgers/${ledger.id}`}>
        <DescriptiveFields defaults={ledger} nameRequired />
      </ResourceForm>
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
