import { useEffect, useRef } from "react";
import { useFetcher } from "react-router";
import { PageHeader, Section } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { Button } from "~/components/ui/button";
import { Field, Input } from "~/components/ui/field";
import { Notice } from "~/components/ui/notice";
import { Table, Td, Th } from "~/components/ui/table";
import { api } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { formatAmount } from "~/lib/format";
import { formError, text } from "~/lib/forms";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import { canWrite } from "~/lib/session";
import type { Currency, List } from "~/lib/types";
import type { Route } from "./+types/currencies";

export const meta: Route.MetaFunction = () => title("Currencies");

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const { cursor } = searchParams(request, []);
  return api<List<Currency>>(withQuery("/v1/currencies", { cursor, limit: 100 }), { signal: request.signal });
}

export async function clientAction({ request }: Route.ClientActionArgs) {
  const form = await request.formData();
  const code = text(form, "code").toUpperCase();
  const exponent = Number(text(form, "exponent"));
  if (!Number.isInteger(exponent) || text(form, "exponent") === "") {
    return { error: "Exponent must be a whole number.", created: null };
  }
  try {
    const currency = await api<Currency>("/v1/currencies", { method: "POST", body: { code, exponent }, signal: request.signal });
    return { error: null, created: currency.code };
  } catch (err) {
    return { ...formError(err), created: null };
  }
}

export default function Currencies({ loaderData: currencies }: Route.ComponentProps) {
  const writable = canWrite(useApiKey().role);
  return (
    <div className="grid gap-8">
      <PageHeader title="Currencies" />
      {writable && <RegisterCurrency />}
      <Section title="Registered">
        <Table>
          <thead>
            <tr>
              <Th>Code</Th>
              <Th numeric>Exponent</Th>
              <Th numeric>Smallest unit</Th>
            </tr>
          </thead>
          <tbody>
            {currencies.data.map((currency) => (
              <tr key={currency.code}>
                <Td className="font-mono">{currency.code}</Td>
                <Td numeric>{currency.exponent}</Td>
                <Td numeric>{formatAmount("1", currency.exponent)}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
        <Pagination nextCursor={currencies.next_cursor} />
      </Section>
    </div>
  );
}

function RegisterCurrency() {
  const fetcher = useFetcher<typeof clientAction>();
  const form = useRef<HTMLFormElement>(null);
  const created = fetcher.data?.created;

  useEffect(() => {
    if (fetcher.state === "idle" && created) form.current?.reset();
  }, [fetcher.state, created]);

  return (
    <Section title="Register a currency">
      <p className="max-w-2xl text-muted">
        ISO 4217 currencies are already registered. Add others, such as ETH or loyalty points, before creating accounts
        in them. A currency's exponent is how many decimal places its amounts have, and it can never change.
      </p>
      <fetcher.Form ref={form} method="post" className="flex flex-wrap items-end gap-3">
        <Field label="Code">
          {(props) => (
            <Input {...props} name="code" required pattern="[A-Za-z][A-Za-z0-9_]{2,15}" title="3-16 letters, digits or underscores, starting with a letter" className="w-40 font-mono uppercase" />
          )}
        </Field>
        <Field label="Exponent">
          {(props) => <Input {...props} name="exponent" type="number" min={0} max={30} required className="w-24" />}
        </Field>
        <Button type="submit" variant="primary" disabled={fetcher.state !== "idle"}>
          Register
        </Button>
      </fetcher.Form>
      {fetcher.data?.error && <Notice tone="danger">{fetcher.data.error}</Notice>}
      {created && fetcher.state === "idle" && <Notice>{created} is registered.</Notice>}
    </Section>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
