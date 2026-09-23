import { CopyValue } from "~/components/copy-value";
import { Amount } from "~/components/amount";
import { DateRangeFields } from "~/components/date-range-fields";
import { FilterBar } from "~/components/filter-bar";
import { PageHeader } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { StatusText } from "~/components/status";
import { Input, Select } from "~/components/ui/field";
import { ButtonLink, TextLink } from "~/components/ui/link";
import { Notice } from "~/components/ui/notice";
import { Table, Td, Th } from "~/components/ui/table";
import { accountsById } from "~/lib/accounts";
import { api, fetchAll } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { formatDateTime } from "~/lib/format";
import { isId } from "~/lib/ids";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import { canWrite } from "~/lib/session";
import { toApiTime } from "~/lib/time";
import type { Account, Ledger, List, Transaction } from "~/lib/types";
import type { Route } from "./+types/index";

export const meta: Route.MetaFunction = () => title("Transactions");

const filterNames = ["ledger_id", "account_id", "status", "external_id", "from", "until"] as const;

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const { cursor, filters } = searchParams(request, filterNames);
  const [ledgers, transactions] = await Promise.all([
    fetchAll<Ledger>("/v1/ledgers", { signal: request.signal }),
    api<List<Transaction>>(
      withQuery("/v1/transactions", {
        ledger_id: isId(filters.ledger_id, "ldg") ? filters.ledger_id : "",
        account_id: isId(filters.account_id, "acct") ? filters.account_id : "",
        status: filters.status,
        external_id: filters.external_id,
        effective_at_lower_bound: toApiTime(filters.from),
        effective_at_upper_bound: toApiTime(filters.until),
        cursor,
        limit: 50,
      }),
      { signal: request.signal },
    ),
  ]);
  const accounts = await accountsById(transactions.data.flatMap((t) => t.entries.map((e) => e.account_id)), request.signal);
  return { ledgers, transactions, accounts, filters };
}

/** Totals a transaction's debits per currency, which is the amount it moves. */
function moved(transaction: Transaction, accounts: Map<string, Account>) {
  const sums = new Map<string, { exponent: number; total: bigint }>();
  for (const entry of transaction.entries) {
    const account = accounts.get(entry.account_id);
    if (!account || entry.side !== "debit") continue;
    const sum = sums.get(account.currency) ?? { exponent: account.currency_exponent, total: 0n };
    sum.total += BigInt(entry.amount);
    sums.set(account.currency, sum);
  }
  return [...sums.entries()];
}

export default function Transactions({ loaderData }: Route.ComponentProps) {
  const { ledgers, transactions, accounts, filters } = loaderData;
  const writable = canWrite(useApiKey().role);
  const ledgerNames = new Map(ledgers.map((l) => [l.id, l.name]));
  const filtered = Object.values(filters).some(Boolean);

  return (
    <div className="grid gap-6">
      <PageHeader
        title="Transactions"
        actions={
          writable && (
            <>
              <ButtonLink to="/transactions/import">Import</ButtonLink>
              <ButtonLink to={withQuery("/transactions/new", { ledger_id: filters.ledger_id })} variant="primary">
                New transaction
              </ButtonLink>
            </>
          )
        }
      />
      <FilterBar label="Filter transactions" active={filtered}>
        <Select name="ledger_id" defaultValue={filters.ledger_id} aria-label="Ledger" className="w-44">
          <option value="">Any ledger</option>
          {ledgers.map((l) => (
            <option key={l.id} value={l.id}>
              {l.name}
            </option>
          ))}
        </Select>
        <Select name="status" defaultValue={filters.status} aria-label="Status" className="w-32">
          <option value="">Any status</option>
          <option value="pending">Pending</option>
          <option value="posted">Posted</option>
          <option value="archived">Archived</option>
        </Select>
        <Input name="account_id" defaultValue={filters.account_id} placeholder="Account ID" aria-label="Account ID" className="w-72 font-mono text-xs" />
        <Input name="external_id" defaultValue={filters.external_id} placeholder="External ID" aria-label="External ID" className="w-40" />
        <DateRangeFields from={filters.from} until={filters.until} />
      </FilterBar>

      {transactions.data.length === 0 ? (
        <Notice>{filtered ? "No transactions match these filters." : "No transactions yet."}</Notice>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Effective</Th>
              <Th>Description</Th>
              <Th>Ledger</Th>
              <Th>Status</Th>
              <Th numeric>Amount</Th>
              <Th>ID</Th>
            </tr>
          </thead>
          <tbody>
            {transactions.data.map((t) => (
              <tr key={t.id}>
                <Td className="whitespace-nowrap">{formatDateTime(t.effective_at)}</Td>
                <Td>
                  <TextLink to={`/transactions/${t.id}`}>{t.description || <span className="text-muted">No description</span>}</TextLink>
                  {t.external_id && <span className="ml-2 font-mono text-xs text-muted">{t.external_id}</span>}
                </Td>
                <Td>{ledgerNames.get(t.ledger_id) ?? <span className="font-mono text-xs">{t.ledger_id}</span>}</Td>
                <Td>
                  <StatusText status={t.status} />
                </Td>
                <Td numeric>
                  {moved(t, accounts).map(([currency, sum]) => (
                    <div key={currency}>
                      <Amount value={String(sum.total)} exponent={sum.exponent} currency={currency} />
                    </div>
                  ))}
                </Td>
                <Td><CopyValue value={t.id} /></Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      <Pagination nextCursor={transactions.next_cursor} />
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
