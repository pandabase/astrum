import { Amount } from "~/components/amount";
import { Details, MetadataValue } from "~/components/details";
import { EntriesTable } from "~/components/entries-table";
import { PageHeader, Section } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { AccountRef, IdLink } from "~/components/refs";
import { accountsById } from "~/lib/accounts";
import { api, path } from "~/lib/api";
import { formatDateTime } from "~/lib/format";
import { routeId } from "~/lib/ids";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import type { Entry, List, Settlement } from "~/lib/types";
import type { Route } from "./+types/detail";

export const meta: Route.MetaFunction = () => title("Settlement");

export async function clientLoader({ params, request }: Route.ClientLoaderArgs) {
  const id = routeId(params.settlementId, "stl");
  const { cursor } = searchParams(request, []);
  const [settlement, entries] = await Promise.all([
    api<Settlement>(path`/v1/settlements/${id}`, { signal: request.signal }),
    api<List<Entry>>(withQuery("/v1/entries", { settlement_id: id, cursor, limit: 100 }), { signal: request.signal }),
  ]);
  const accounts = await accountsById([settlement.settled_account_id, settlement.contra_account_id], request.signal);
  return { settlement, entries, accounts };
}

export default function SettlementDetail({ loaderData }: Route.ComponentProps) {
  const { settlement: s, entries, accounts } = loaderData;
  const exponent = accounts.get(s.settled_account_id)?.currency_exponent ?? 0;
  return (
    <div className="grid gap-8">
      <PageHeader title={s.description || "Settlement"} />
      <Details
        items={[
          { term: "Amount", value: <Amount value={s.amount} exponent={exponent} currency={s.currency} /> },
          { term: "Settled account", value: <AccountRef id={s.settled_account_id} accounts={accounts} /> },
          { term: "Contra account", value: <AccountRef id={s.contra_account_id} accounts={accounts} /> },
          { term: "Transaction", value: s.transaction_id ? <IdLink id={s.transaction_id} /> : <span className="text-muted">None; the net was zero</span> },
          { term: "Entries covered", value: s.entry_count },
          { term: "Cut-off", value: s.effective_at_upper_bound ? formatDateTime(s.effective_at_upper_bound) : "None" },
          { term: "Created", value: formatDateTime(s.created_at) },
          { term: "ID", value: s.id, mono: true },
          { term: "Idempotency key", value: s.idempotency_key, mono: true },
          { term: "Metadata", value: <MetadataValue metadata={s.metadata} /> },
        ]}
      />
      <Section title="Settled entries">
        <EntriesTable entries={entries.data} accounts={accounts} />
        <Pagination nextCursor={entries.next_cursor} />
      </Section>
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
