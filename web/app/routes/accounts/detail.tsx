import { ActionButton, ActionError, ConfirmAction } from "~/components/confirm-action";
import { Amount } from "~/components/amount";
import { BalanceTable } from "~/components/balance-table";
import { Details, MetadataValue } from "~/components/details";
import { DateRangeFields } from "~/components/date-range-fields";
import { FilterBar } from "~/components/filter-bar";
import { PageHeader, Section } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { IdLink } from "~/components/refs";
import { StatusText } from "~/components/status";
import { ButtonLink, TextLink } from "~/components/ui/link";
import { Notice } from "~/components/ui/notice";
import { Table, Td, Th } from "~/components/ui/table";
import { api, fetchAll, path } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { formatDateTime } from "~/lib/format";
import { formError, text } from "~/lib/forms";
import { routeId } from "~/lib/ids";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import { canWrite } from "~/lib/session";
import { toApiTime } from "~/lib/time";
import type { Account, AccountBalances, AccountEntry, Hold, Ledger, List } from "~/lib/types";
import type { Route } from "./+types/detail";

export const meta: Route.MetaFunction = ({ loaderData }) => title(loaderData?.account.code ?? "Account");

export async function clientLoader({ params, request }: Route.ClientLoaderArgs) {
  const id = routeId(params.accountId, "acct");
  const { cursor, filters } = searchParams(request, ["from", "until"]);
  const signal = request.signal;
  const from = toApiTime(filters.from);
  const until = toApiTime(filters.until);
  const [account, entries, holds, windowed] = await Promise.all([
    api<Account>(path`/v1/accounts/${id}`, { signal }),
    api<List<AccountEntry>>(withQuery(path`/v1/accounts/${id}/entries`, { cursor, limit: 50 }), { signal }),
    fetchAll<Hold>(withQuery("/v1/holds", { account_id: id, status: "pending" }), { signal, max: 100 }),
    from || until
      ? api<AccountBalances>(withQuery(path`/v1/accounts/${id}/balances`, { effective_at_lower_bound: from, effective_at_upper_bound: until }), { signal })
      : null,
  ]);
  const ledger = await api<Ledger>(path`/v1/ledgers/${account.ledger_id}`, { signal });
  return { account, entries, holds, windowed, ledger, filters };
}

const fetcherKey = "account-status";

export async function clientAction({ params, request }: Route.ClientActionArgs) {
  const id = routeId(params.accountId, "acct");
  const intent = text(await request.formData(), "intent");
  if (!["freeze", "unfreeze", "close"].includes(intent)) return { error: "Unknown action." };
  try {
    await api<Account>(path`/v1/accounts/${id}/${intent}`, { method: "POST", signal: request.signal });
  } catch (err) {
    return formError(err);
  }
  return null;
}

export default function AccountDetail({ loaderData }: Route.ComponentProps) {
  const { account, entries, holds, windowed, ledger, filters } = loaderData;
  const writable = canWrite(useApiKey().role) && account.status !== "closed";
  const exponent = account.currency_exponent;
  const inLedger = { ledger_id: ledger.id, account_id: account.id };

  return (
    <div className="grid gap-8">
      <PageHeader
        title={account.name || account.code}
        actions={
          writable && (
            <>
              <ButtonLink to={`/accounts/${account.id}/edit`}>Edit</ButtonLink>
              {account.status === "open" && (
                <ActionButton fetcherKey={fetcherKey} intent="freeze">
                  Freeze
                </ActionButton>
              )}
              {account.status === "frozen" && (
                <ActionButton fetcherKey={fetcherKey} intent="unfreeze">
                  Unfreeze
                </ActionButton>
              )}
              <ConfirmAction fetcherKey={fetcherKey} intent="close" label="Close" title={`Close ${account.code}?`} confirmLabel="Close account" variant="danger">
                <p>Closing is permanent. The account must have a zero balance and no holds, and it will never accept money again.</p>
              </ConfirmAction>
            </>
          )
        }
      />
      <ActionError fetcherKey={fetcherKey} />

      <Section title="Balances">
        <BalanceTable balances={account.balances} exponent={exponent} currency={account.currency} />
        {account.held !== "0" && (
          <p className="text-muted">
            <Amount value={account.held} exponent={exponent} currency={account.currency} /> is reserved by holds.
          </p>
        )}
      </Section>

      {writable && account.status === "open" && (
        <div className="flex flex-wrap gap-2">
          <ButtonLink to={withQuery("/transactions/new", { ledger_id: ledger.id })}>New transaction</ButtonLink>
          <ButtonLink to={withQuery("/holds/new", inLedger)}>New hold</ButtonLink>
          <ButtonLink to={withQuery("/settlements/new", inLedger)}>Settle</ButtonLink>
          <ButtonLink to={withQuery("/statements/new", inLedger)}>New statement</ButtonLink>
          <ButtonLink to={withQuery("/monitors/new", inLedger)}>New monitor</ButtonLink>
        </div>
      )}

      <Section title="Balances for a period">
        <p className="text-muted">Counts transactions by their effective date, so backdated ones land on the day they count. Holds are left out.</p>
        <FilterBar label="Balance period" active={Boolean(windowed)}>
          <DateRangeFields from={filters.from} until={filters.until} />
        </FilterBar>
        {windowed && <BalanceTable balances={windowed} exponent={exponent} currency={account.currency} />}
      </Section>

      {holds.length > 0 && (
        <Section title="Pending holds">
          <Table>
            <thead>
              <tr>
                <Th>Description</Th>
                <Th numeric>Amount</Th>
                <Th>Expires</Th>
              </tr>
            </thead>
            <tbody>
              {holds.map((h) => (
                <tr key={h.id}>
                  <Td>
                    <TextLink to={`/holds/${h.id}`}>{h.description || "Hold"}</TextLink>
                  </Td>
                  <Td numeric>
                    <Amount value={h.amount} exponent={exponent} />
                  </Td>
                  <Td>{formatDateTime(h.expires_at)}</Td>
                </tr>
              ))}
            </tbody>
          </Table>
        </Section>
      )}

      <Details
        items={[
          { term: "Ledger", value: <TextLink to={`/ledgers/${ledger.id}`}>{ledger.name}</TextLink> },
          { term: "Code", value: account.code, mono: true },
          { term: "ID", value: account.id, mono: true },
          { term: "Status", value: <StatusText status={account.status} /> },
          { term: "Currency", value: account.currency },
          { term: "Normal side", value: account.normal_side },
          {
            term: "Negative balance",
            value: account.allow_negative ? (
              "Allowed"
            ) : account.overdraft_limit !== "0" ? (
              <>
                Down to <Amount value={`-${account.overdraft_limit}`} exponent={exponent} currency={account.currency} />
              </>
            ) : (
              "Not allowed"
            ),
          },
          { term: "Description", value: account.description || <span className="text-muted">None</span> },
          { term: "Lock version", value: account.lock_version },
          { term: "Created", value: formatDateTime(account.created_at) },
          { term: "Metadata", value: <MetadataValue metadata={account.metadata} /> },
          {
            term: "Related",
            value: (
              <span className="flex flex-wrap gap-x-4">
                <TextLink to={withQuery("/transactions", { account_id: account.id })}>Transactions</TextLink>
                <TextLink to={withQuery("/entries", { account_id: account.id })}>Entries</TextLink>
                <TextLink to={withQuery("/entries", { account_id: account.id, settled: "false", status: "posted" })}>Unsettled entries</TextLink>
                <TextLink to={withQuery("/holds", { account_id: account.id })}>Holds</TextLink>
                <TextLink to={withQuery("/statements", { account_id: account.id })}>Statements</TextLink>
                <TextLink to={withQuery("/settlements", { account_id: account.id })}>Settlements</TextLink>
                <TextLink to={withQuery("/monitors", { account_id: account.id })}>Monitors</TextLink>
              </span>
            ),
          },
        ]}
      />

      <Section title="Entries">
        {entries.data.length === 0 ? (
          <Notice>No posted entries yet.</Notice>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>Posted</Th>
                <Th>Transaction</Th>
                <Th numeric>Debit</Th>
                <Th numeric>Credit</Th>
                <Th numeric>Balance</Th>
              </tr>
            </thead>
            <tbody>
              {entries.data.map((entry, i) => (
                <tr key={`${entry.transaction_id}-${i}`}>
                  <Td className="whitespace-nowrap">{formatDateTime(entry.created_at)}</Td>
                  <Td>
                    <IdLink id={entry.transaction_id} />
                  </Td>
                  <Td numeric>{entry.side === "debit" && <Amount value={entry.amount} exponent={exponent} />}</Td>
                  <Td numeric>{entry.side === "credit" && <Amount value={entry.amount} exponent={exponent} />}</Td>
                  <Td numeric>
                    <Amount value={entry.balance_after} exponent={exponent} />
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
        <Pagination nextCursor={entries.next_cursor} />
      </Section>
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
