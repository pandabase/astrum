import { redirect } from "react-router";
import { PageHeader } from "~/components/page";
import { DescriptiveFields, ResourceForm } from "~/components/resource-form";
import { api, path } from "~/lib/api";
import { descriptivePatch, formError, readDescriptive, readOriginal } from "~/lib/forms";
import { routeId } from "~/lib/ids";
import { title } from "~/lib/meta";
import type { Account } from "~/lib/types";
import type { Route } from "./+types/edit";

export const meta: Route.MetaFunction = ({ loaderData }) => title(`Edit ${loaderData?.code ?? "account"}`);

export async function clientLoader({ params, request }: Route.ClientLoaderArgs) {
  const accountId = routeId(params.accountId, "acct");
  return api<Account>(path`/v1/accounts/${accountId}`, { signal: request.signal });
}

export async function clientAction({ params, request }: Route.ClientActionArgs) {
  const accountId = routeId(params.accountId, "acct");
  const form = await request.formData();
  const details = readDescriptive(form);
  if (!details.ok) return details;
  const patch = descriptivePatch(readOriginal(form), details.value);
  try {
    if (Object.keys(patch).length > 0) {
      await api<Account>(path`/v1/accounts/${accountId}`, { method: "PATCH", body: patch, signal: request.signal });
    }
  } catch (err) {
    return formError(err);
  }
  throw redirect(`/accounts/${accountId}`);
}

export default function EditAccount({ loaderData: account, actionData }: Route.ComponentProps) {
  return (
    <div className="grid gap-6">
      <PageHeader title={`Edit ${account.code}`} />
      <ResourceForm error={actionData?.error} submitLabel="Save changes" cancelTo={`/accounts/${account.id}`}>
        <DescriptiveFields defaults={account} />
      </ResourceForm>
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
