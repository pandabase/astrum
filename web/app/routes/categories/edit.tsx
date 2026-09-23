import { redirect } from "react-router";
import { PageHeader } from "~/components/page";
import { DescriptiveFields, ResourceForm } from "~/components/resource-form";
import { api, path } from "~/lib/api";
import { descriptivePatch, formError, readDescriptive, readOriginal } from "~/lib/forms";
import { routeId } from "~/lib/ids";
import { title } from "~/lib/meta";
import type { Category } from "~/lib/types";
import type { Route } from "./+types/edit";

export const meta: Route.MetaFunction = () => title("Edit category");

export async function clientLoader({ params, request }: Route.ClientLoaderArgs) {
  return api<Category>(path`/v1/account_categories/${routeId(params.categoryId, "cat")}`, { signal: request.signal });
}

export async function clientAction({ params, request }: Route.ClientActionArgs) {
  const id = routeId(params.categoryId, "cat");
  const form = await request.formData();
  const details = readDescriptive(form);
  if (!details.ok) return details;
  const patch = descriptivePatch(readOriginal(form), details.value);
  try {
    if (Object.keys(patch).length > 0) {
      await api(path`/v1/account_categories/${id}`, { method: "PATCH", body: patch, signal: request.signal });
    }
  } catch (err) {
    return formError(err);
  }
  throw redirect(`/categories/${id}`);
}

export default function EditCategory({ loaderData: category, actionData }: Route.ComponentProps) {
  return (
    <div className="grid gap-6">
      <PageHeader title={`Edit ${category.name}`} />
      <ResourceForm error={actionData?.error} submitLabel="Save changes" cancelTo={`/categories/${category.id}`}>
        <DescriptiveFields defaults={category} nameRequired />
      </ResourceForm>
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
