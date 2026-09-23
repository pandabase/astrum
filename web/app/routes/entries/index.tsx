import { EntriesTable } from "~/components/entries-table";
import { DateRangeFields } from "~/components/date-range-fields";
import { FilterBar } from "~/components/filter-bar";
import { PageHeader } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { Input, Select } from "~/components/ui/field";
import { Notice } from "~/components/ui/notice";
import { accountsById } from "~/lib/accounts";
import { api, fetchAll } from "~/lib/api";
import { isId, type IdPrefix } from "~/lib/ids";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import { toApiTime } from "~/lib/time";
import type { Entry, Ledger, List } from "~/lib/types";
import type { Route } from "./+types/index";

export const meta: Route.MetaFunction = () => title("Entries");

const filterNames = ["ledger_id", "account_id", "transaction_id", "statement_id", "settlement_id", "status", "side", "settled", "from", "until"] as const;
const idFilters: [(typeof filterNames)[number], IdPrefix][] = [
  ["ledger_id", "ldg"],
  ["account_id", "acct"],
  ["transaction_id", "txn"],
  ["statement_id", "stmt"],
  ["settlement_id", "stl"],
];

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const { cursor, filters } = searchParams(request, filterNames);
  // Malformed ids are dropped rather than sent, so a typo shows everything instead of an error page.
  const ids = Object.fromEntries(idFilters.map(([name, prefix]) => [name, isId(filters[name], prefix) ? filters[name] : ""]));
  const [ledgers, entries] = await Promise.all([
    fetchAll<Ledger>("/v1/ledgers", { signal: request.signal }),
    api<List<Entry>>(
      withQuery("/v1/entries", {
        ...ids,
        status: filters.status,
        side: filters.side,
        settled: filters.settled,
        effective_at_lower_bound: toApiTime(filters.from),
        effective_at_upper_bound: toApiTime(filters.until),
        cursor,
        limit: 100,
      }),
      { signal: request.signal },
    ),
  ]);
  const accounts = await accountsById(entries.data.map((e) => e.account_id), request.signal);
  return { ledgers, entries, accounts, filters };
}

export default function Entries({ loaderData }: Route.ComponentProps) {
  const { ledgers, entries, accounts, filters } = loaderData;
  const filtered = Object.values(filters).some(Boolean);
  return (
    <div className="grid gap-6">
      <PageHeader title="Entries" />
      <p className="max-w-2xl text-muted">Every posting across ledgers, oldest first. Filter by account, transaction, statement or settlement.</p>
      <FilterBar label="Filter entries" active={filtered}>
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
          <option value="posted">Posted</option>
          <option value="pending">Pending</option>
        </Select>
        <Select name="side" defaultValue={filters.side} aria-label="Side" className="w-28">
          <option value="">Any side</option>
          <option value="debit">Debit</option>
          <option value="credit">Credit</option>
        </Select>
        <Select name="settled" defaultValue={filters.settled} aria-label="Settled" className="w-36">
          <option value="">Settled or not</option>
          <option value="true">Settled</option>
          <option value="false">Unsettled</option>
        </Select>
        {idFilters.slice(1).map(([name]) => (
          <Input
            key={name}
            name={name}
            defaultValue={filters[name]}
            placeholder={`${name.replace("_id", "").replace(/^./, (c) => c.toUpperCase())} ID`}
            aria-label={`${name.replace("_id", "")} ID`}
            className="w-64 font-mono text-xs"
          />
        ))}
        <DateRangeFields from={filters.from} until={filters.until} />
      </FilterBar>
      {entries.data.length === 0 ? <Notice>No entries match.</Notice> : <EntriesTable entries={entries.data} accounts={accounts} />}
      <Pagination nextCursor={entries.next_cursor} />
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
