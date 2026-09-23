import { EventTypeFields } from "~/components/event-type-fields";
import { PageHeader } from "~/components/page";
import { ResourceForm } from "~/components/resource-form";
import { OneTimeSecret } from "~/components/secret";
import { Field, Input } from "~/components/ui/field";
import { TextLink } from "~/components/ui/link";
import { api } from "~/lib/api";
import { formError, text } from "~/lib/forms";
import { title } from "~/lib/meta";
import type { WebhookEndpoint } from "~/lib/types";
import type { Route } from "./+types/new";

export const meta: Route.MetaFunction = () => title("New webhook endpoint");

export async function clientAction({ request }: Route.ClientActionArgs) {
  const form = await request.formData();
  try {
    const endpoint = await api<WebhookEndpoint>("/v1/webhook_endpoints", {
      method: "POST",
      body: { url: text(form, "url"), description: text(form, "description"), event_types: form.getAll("event_types").map(String) },
      signal: request.signal,
    });
    return { error: null, endpoint };
  } catch (err) {
    return { ...formError(err), endpoint: null };
  }
}

export default function NewWebhook({ actionData }: Route.ComponentProps) {
  const endpoint = actionData?.endpoint;
  // The signing secret only exists in this response, so the page shows it instead of redirecting.
  if (endpoint?.secret) {
    return (
      <div className="grid max-w-2xl gap-6">
        <PageHeader title="Endpoint created" />
        <OneTimeSecret label="Signing secret" value={endpoint.secret} />
        <p>
          Verify each request's <code className="font-mono">Astrum-Signature</code> header with this secret.{" "}
          <TextLink to={`/webhooks/${endpoint.id}`}>Go to the endpoint</TextLink>
        </p>
      </div>
    );
  }
  return (
    <div className="grid gap-6">
      <PageHeader title="New webhook endpoint" />
      <ResourceForm error={actionData?.error ?? undefined} submitLabel="Create endpoint" cancelTo="/webhooks">
        <Field label="URL" hint="Must be https and reachable on the public internet.">
          {(props) => <Input {...props} name="url" type="url" required placeholder="https://example.com/webhooks/astrum" className="font-mono" />}
        </Field>
        <Field label="Description">{(props) => <Input {...props} name="description" maxLength={1000} />}</Field>
        <EventTypeFields />
      </ResourceForm>
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
