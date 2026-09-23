import { useFetcher } from "react-router";
import { Amount } from "~/components/amount";
import { ActionError, ConfirmAction } from "~/components/confirm-action";
import { Details, MetadataValue } from "~/components/details";
import { PageHeader, Section } from "~/components/page";
import { AccountRef, IdLink } from "~/components/refs";
import { StatusText } from "~/components/status";
import { Field, Input } from "~/components/ui/field";
import { ButtonLink, TextLink } from "~/components/ui/link";
import { Notice } from "~/components/ui/notice";
import { Table, Td, Th } from "~/components/ui/table";
import { accountsById } from "~/lib/accounts";
import { api, path } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { formatAmount, formatDateTime, parseAmount } from "~/lib/format";
import { formError, text } from "~/lib/forms";
import { routeId } from "~/lib/ids";
import { useIdempotencyKey } from "~/lib/idempotency";
import { title } from "~/lib/meta";
import { canWrite } from "~/lib/session";
import type { EntryLine, Ledger, Transaction } from "~/lib/types";
import type { Route } from "./+types/detail";

export const meta: Route.MetaFunction = ({ loaderData }) => title(loaderData?.transaction.description || "Transaction");

export async function clientLoader({ params, request }: Route.ClientLoaderArgs) {
  const id = routeId(params.transactionId, "txn");
  const transaction = await api<Transaction>(path`/v1/transactions/${id}`, { signal: request.signal });
  const [ledger, accounts] = await Promise.all([
    api<Ledger>(path`/v1/ledgers/${transaction.ledger_id}`, { signal: request.signal }),
    accountsById(transaction.entries.map((e) => e.account_id), request.signal),
  ]);
  return { transaction, ledger, accounts };
}

const fetcherKey = "transaction-action";

export async function clientAction({ params, request }: Route.ClientActionArgs) {
  const id = routeId(params.transactionId, "txn");
  const form = await request.formData();
  const intent = text(form, "intent");
  try {
    switch (intent) {
      case "post": {
        const transaction = await api<Transaction>(path`/v1/transactions/${id}`, { signal: request.signal });
        const accounts = await accountsById(transaction.entries.map((e) => e.account_id), request.signal);
        const entries: EntryLine[] = [];
        for (const [i, entry] of transaction.entries.entries()) {
          const exponent = accounts.get(entry.account_id)?.currency_exponent ?? 0;
          const amount = parseAmount(text(form, `amount_${i}`), exponent);
          if (amount === null || amount.startsWith("-")) {
            return { error: `Entry ${i + 1}: enter an amount between 0 and the pending amount.` };
          }
          if (amount !== "0") entries.push({ account_id: entry.account_id, side: entry.side, amount });
        }
        const partial = entries.length !== transaction.entries.length || entries.some((e, i) => e.amount !== transaction.entries[i].amount);
        await api(path`/v1/transactions/${id}/post`, { method: "POST", body: partial ? { entries } : undefined, signal: request.signal });
        return { error: null, reversal: null };
      }
      case "archive":
        await api(path`/v1/transactions/${id}/archive`, { method: "POST", signal: request.signal });
        return { error: null, reversal: null };
      case "reverse": {
        const reversal = await api<Transaction>(path`/v1/transactions/${id}/reverse`, {
          method: "POST",
          body: { description: text(form, "description") },
          idempotencyKey: text(form, "idempotency_key"),
          signal: request.signal,
        });
        return { error: null, reversal: reversal.id };
      }
    }
  } catch (err) {
    return { ...formError(err), reversal: null };
  }
  return { error: "Unknown action.", reversal: null };
}

export default function TransactionDetail({ loaderData }: Route.ComponentProps) {
  const { transaction: t, ledger, accounts } = loaderData;
  const writable = canWrite(useApiKey().role);
  const result = useFetcher<typeof clientAction>({ key: fetcherKey }).data;
  const reversal = result?.reversal;
  const reverseKey = useIdempotencyKey(result);

  return (
    <div className="grid gap-8">
      <PageHeader
        title={t.description || "Transaction"}
        actions={
          writable && (
            <>
              {t.status === "pending" && (
                <>
                  <ButtonLink to={`/transactions/${t.id}/edit`}>Edit</ButtonLink>
                  <PostAction transaction={t} accounts={accounts} />
                  <ConfirmAction fetcherKey={fetcherKey} intent="archive" label="Archive" title="Archive this transaction?" confirmLabel="Archive" variant="danger">
                    <p>Archiving releases every pending amount without posting anything. It cannot be undone.</p>
                  </ConfirmAction>
                </>
              )}
              {t.status === "posted" && (
                <ConfirmAction fetcherKey={fetcherKey} intent="reverse" label="Reverse" title="Reverse this transaction?" confirmLabel="Post reversal">
                  <p>Posts a new transaction with every entry on the opposite side. The original stays in the journal.</p>
                  <input type="hidden" name="idempotency_key" value={reverseKey} />
                  <Field label="Description">
                    {(props) => <Input {...props} name="description" defaultValue={`Reversal of ${t.description || t.id}`} />}
                  </Field>
                </ConfirmAction>
              )}
            </>
          )
        }
      />
      <ActionError fetcherKey={fetcherKey} />
      {reversal && (
        <Notice>
          Reversed by <IdLink id={reversal} />.
        </Notice>
      )}

      <Section title="Entries">
        <Table>
          <thead>
            <tr>
              <Th>Account</Th>
              <Th numeric>Debit</Th>
              <Th numeric>Credit</Th>
              <Th>Currency</Th>
            </tr>
          </thead>
          <tbody>
            {t.entries.map((entry, i) => {
              const account = accounts.get(entry.account_id);
              const exponent = account?.currency_exponent ?? 0;
              return (
                <tr key={i}>
                  <Td>
                    <AccountRef id={entry.account_id} accounts={accounts} />
                    {account?.name && <span className="ml-2 text-muted">{account.name}</span>}
                  </Td>
                  <Td numeric>{entry.side === "debit" && <Amount value={entry.amount} exponent={exponent} />}</Td>
                  <Td numeric>{entry.side === "credit" && <Amount value={entry.amount} exponent={exponent} />}</Td>
                  <Td className="text-muted">{account?.currency}</Td>
                </tr>
              );
            })}
          </tbody>
        </Table>
      </Section>

      <Details
        items={[
          { term: "Status", value: <StatusText status={t.status} /> },
          { term: "Ledger", value: <TextLink to={`/ledgers/${ledger.id}`}>{ledger.name}</TextLink> },
          { term: "ID", value: t.id, mono: true },
          { term: "Effective", value: formatDateTime(t.effective_at) },
          { term: "Created", value: formatDateTime(t.created_at) },
          ...(t.posted_at ? [{ term: "Posted", value: formatDateTime(t.posted_at) }] : []),
          ...(t.archived_at ? [{ term: "Archived", value: formatDateTime(t.archived_at) }] : []),
          { term: "Version", value: t.version },
          { term: "External ID", value: t.external_id ?? <span className="text-muted">None</span>, mono: Boolean(t.external_id) },
          { term: "Idempotency key", value: t.idempotency_key, mono: true },
          ...(t.reverses_id ? [{ term: "Reverses", value: <IdLink id={t.reverses_id} /> }] : []),
          { term: "Metadata", value: <MetadataValue metadata={t.metadata} /> },
          { term: "Entries", value: <TextLink to={`/entries?transaction_id=${t.id}`}>Show in the entries search</TextLink> },
        ]}
      />
    </div>
  );
}

function PostAction({ transaction, accounts }: { transaction: Transaction; accounts: Map<string, { currency_exponent: number; code: string }> }) {
  return (
    <ConfirmAction fetcherKey={fetcherKey} intent="post" label="Post" title="Post this transaction" confirmLabel="Post">
      <p>Post the full amounts, or lower any of them to post part. Whatever is not posted is released.</p>
      <div className="grid gap-2">
        {transaction.entries.map((entry, i) => {
          const account = accounts.get(entry.account_id);
          const exponent = account?.currency_exponent ?? 0;
          return (
            <Field key={i} label={`${entry.side === "debit" ? "Debit" : "Credit"} ${account?.code ?? entry.account_id}`}>
              {(props) => (
                <Input
                  {...props}
                  name={`amount_${i}`}
                  defaultValue={formatAmount(entry.amount, exponent).replaceAll(",", "")}
                  inputMode="decimal"
                  className="text-right tabular-nums"
                />
              )}
            </Field>
          );
        })}
      </div>
    </ConfirmAction>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
