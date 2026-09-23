import { useFetcher } from "react-router";
import { AccountSelect } from "~/components/account-select";
import { Amount } from "~/components/amount";
import { ActionError, ConfirmAction } from "~/components/confirm-action";
import { Details } from "~/components/details";
import { PageHeader } from "~/components/page";
import { AccountRef, IdLink } from "~/components/refs";
import { StatusText } from "~/components/status";
import { Field, Input } from "~/components/ui/field";
import { accountsById, ledgerAccounts } from "~/lib/accounts";
import { api, path } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { formatAmount, formatDateTime, parseAmount } from "~/lib/format";
import { formError, text } from "~/lib/forms";
import { routeId } from "~/lib/ids";
import { useIdempotencyKey } from "~/lib/idempotency";
import { title } from "~/lib/meta";
import { canWrite } from "~/lib/session";
import type { Hold } from "~/lib/types";
import type { Route } from "./+types/detail";

export const meta: Route.MetaFunction = () => title("Hold");

export async function clientLoader({ params, request }: Route.ClientLoaderArgs) {
  const id = routeId(params.holdId, "hold");
  const hold = await api<Hold>(path`/v1/holds/${id}`, { signal: request.signal });
  const account = (await accountsById([hold.account_id], request.signal)).get(hold.account_id);
  const destinations = account && hold.status === "pending" ? (await ledgerAccounts(account.ledger_id, request.signal)).filter((a) => a.id !== account.id && a.currency === account.currency) : [];
  return { hold, account, destinations };
}

const fetcherKey = "hold-action";

export async function clientAction({ params, request }: Route.ClientActionArgs) {
  const id = routeId(params.holdId, "hold");
  const form = await request.formData();
  try {
    if (text(form, "intent") === "void") {
      await api(path`/v1/holds/${id}/void`, { method: "POST", signal: request.signal });
      return null;
    }
    const hold = await api<Hold>(path`/v1/holds/${id}`, { signal: request.signal });
    const exponent = (await accountsById([hold.account_id], request.signal)).get(hold.account_id)?.currency_exponent ?? 0;
    const amount = parseAmount(text(form, "amount"), exponent);
    if (amount === null || amount.startsWith("-") || amount === "0") return { error: "Enter a positive amount up to the held amount." };
    await api(path`/v1/holds/${id}/capture`, {
      method: "POST",
      body: { destination_account_id: text(form, "destination_account_id"), amount, description: text(form, "description") },
      idempotencyKey: text(form, "idempotency_key"),
      signal: request.signal,
    });
    return null;
  } catch (err) {
    return formError(err);
  }
}

export default function HoldDetail({ loaderData }: Route.ComponentProps) {
  const { hold, account, destinations } = loaderData;
  const captureKey = useIdempotencyKey(useFetcher({ key: fetcherKey }).data);
  const exponent = account?.currency_exponent ?? 0;
  const actionable = canWrite(useApiKey().role) && hold.status === "pending";

  return (
    <div className="grid gap-8">
      <PageHeader
        title={hold.description || "Hold"}
        actions={
          actionable && (
            <>
              <ConfirmAction fetcherKey={fetcherKey} intent="capture" label="Capture" title="Capture this hold" confirmLabel="Capture">
                <p>Moves up to the held amount to another account in a posted transaction and releases the rest.</p>
                <input type="hidden" name="idempotency_key" value={captureKey} />
                <Field label="Destination">{(props) => <AccountSelect {...props} name="destination_account_id" accounts={destinations} required />}</Field>
                <Field label="Amount">
                  {(props) => (
                    <Input {...props} name="amount" inputMode="decimal" defaultValue={formatAmount(hold.amount, exponent).replaceAll(",", "")} className="text-right tabular-nums" />
                  )}
                </Field>
                <Field label="Description">{(props) => <Input {...props} name="description" />}</Field>
              </ConfirmAction>
              <ConfirmAction fetcherKey={fetcherKey} intent="void" label="Void" title="Void this hold?" confirmLabel="Void hold" variant="danger">
                <p>Releases the whole held amount. Nothing is posted.</p>
              </ConfirmAction>
            </>
          )
        }
      />
      <ActionError fetcherKey={fetcherKey} />
      <Details
        items={[
          { term: "Status", value: <StatusText status={hold.status} /> },
          { term: "Amount", value: <Amount value={hold.amount} exponent={exponent} currency={hold.currency} /> },
          ...(hold.captured_amount ? [{ term: "Captured", value: <Amount value={hold.captured_amount} exponent={exponent} currency={hold.currency} /> }] : []),
          ...(hold.capture_transaction_id ? [{ term: "Capture", value: <IdLink id={hold.capture_transaction_id} /> }] : []),
          { term: "Account", value: <AccountRef id={hold.account_id} accounts={account ? new Map([[account.id, account]]) : undefined} /> },
          { term: "Expires", value: formatDateTime(hold.expires_at) },
          { term: "Created", value: formatDateTime(hold.created_at) },
          ...(hold.resolved_at ? [{ term: "Resolved", value: formatDateTime(hold.resolved_at) }] : []),
          { term: "ID", value: hold.id, mono: true },
          { term: "Idempotency key", value: hold.idempotency_key, mono: true },
        ]}
      />
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
