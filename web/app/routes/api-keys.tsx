import { useFetcher } from "react-router";
import { ActionError, ConfirmAction } from "~/components/confirm-action";
import { PageHeader, Section } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { OneTimeSecret } from "~/components/secret";
import { Button } from "~/components/ui/button";
import { Field, Input, Select } from "~/components/ui/field";
import { Notice } from "~/components/ui/notice";
import { Table, Td, Th } from "~/components/ui/table";
import { api, path } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { formatDateTime } from "~/lib/format";
import { formError, text } from "~/lib/forms";
import { isId } from "~/lib/ids";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import { toApiTime } from "~/lib/time";
import type { ApiKey, CreatedApiKey, List } from "~/lib/types";
import type { Route } from "./+types/api-keys";

export const meta: Route.MetaFunction = () => title("API keys");

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const { cursor } = searchParams(request, []);
  return api<List<ApiKey>>(withQuery("/v1/api_keys", { cursor, limit: 50 }), { signal: request.signal });
}

export async function clientAction({ request }: Route.ClientActionArgs) {
  const form = await request.formData();
  try {
    if (text(form, "intent") === "revoke") {
      const id = text(form, "key_id");
      if (!isId(id, "key")) return { error: "Unknown key.", created: null };
      await api(path`/v1/api_keys/${id}/revoke`, { method: "POST", signal: request.signal });
      return { error: null, created: null };
    }
    const expiresText = text(form, "expires_at");
    const expiresAt = toApiTime(expiresText);
    if (expiresText && !expiresAt) return { error: "The expiry is not a valid date.", created: null };
    const created = await api<CreatedApiKey>("/v1/api_keys", {
      method: "POST",
      body: { name: text(form, "name"), role: text(form, "role"), ...(expiresAt ? { expires_at: expiresAt } : {}) },
      signal: request.signal,
    });
    return { error: null, created };
  } catch (err) {
    return { ...formError(err), created: null };
  }
}

export default function ApiKeys({ loaderData: keys }: Route.ComponentProps) {
  const self = useApiKey();
  const create = useFetcher<typeof clientAction>({ key: "create-key" });
  const created = create.data?.created;
  return (
    <div className="grid gap-8">
      <PageHeader title="API keys" />
      <Section title="Create a key">
        <create.Form method="post" className="flex flex-wrap items-end gap-3">
          <Field label="Name">{(props) => <Input {...props} name="name" required maxLength={255} className="w-56" />}</Field>
          <Field label="Role">
            {(props) => (
              <Select {...props} name="role" defaultValue="write" className="w-36">
                <option value="read">read</option>
                <option value="write">write</option>
                <option value="admin">admin</option>
              </Select>
            )}
          </Field>
          <Field label="Expires">{(props) => <Input {...props} name="expires_at" type="datetime-local" className="w-56" />}</Field>
          <Button type="submit" variant="primary" disabled={create.state !== "idle"}>
            Create key
          </Button>
        </create.Form>
        <p className="text-muted">Read keys can only look. Write keys can do everything except manage keys. Admin keys can do everything.</p>
        {create.data?.error && <Notice tone="danger">{create.data.error}</Notice>}
        {created && <OneTimeSecret label={`Secret for ${created.name} (${created.role})`} value={created.secret} />}
      </Section>

      <Section title="Keys">
        <ActionError fetcherKey="revoke-key" />
        <Table>
          <thead>
            <tr>
              <Th>Name</Th>
              <Th>Role</Th>
              <Th>Key</Th>
              <Th>Last used</Th>
              <Th>Expires</Th>
              <Th>Status</Th>
              <Th />
            </tr>
          </thead>
          <tbody>
            {keys.data.map((k) => {
              const expired = k.expires_at !== null && new Date(k.expires_at) <= new Date();
              return (
                <tr key={k.id}>
                  <Td>
                    {k.name}
                    {k.id === self.id && <span className="ml-2 text-muted">(this session)</span>}
                  </Td>
                  <Td>{k.role}</Td>
                  <Td className="font-mono text-xs">
                    {k.id} {k.hint}
                  </Td>
                  <Td className="whitespace-nowrap">{k.last_used_at ? formatDateTime(k.last_used_at) : <span className="text-muted">Never</span>}</Td>
                  <Td className="whitespace-nowrap">{k.expires_at ? formatDateTime(k.expires_at) : <span className="text-muted">Never</span>}</Td>
                  <Td>{k.revoked_at ? <span className="text-muted">revoked</span> : expired ? <span className="text-muted">expired</span> : "active"}</Td>
                  <Td className="w-0">
                    {!k.revoked_at && (
                      <ConfirmAction fetcherKey="revoke-key" intent="revoke" label="Revoke" title={`Revoke ${k.name}?`} confirmLabel="Revoke key" variant="danger">
                        <input type="hidden" name="key_id" value={k.id} />
                        <p>
                          Requests with this key stop working within 10 seconds, and it can never be used again.
                          {k.id === self.id && " This is the key you are signed in with, so you will be signed out."}
                        </p>
                      </ConfirmAction>
                    )}
                  </Td>
                </tr>
              );
            })}
          </tbody>
        </Table>
        <Pagination nextCursor={keys.next_cursor} />
      </Section>
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
