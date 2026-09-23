import { Amount } from "~/components/amount";
import { Details } from "~/components/details";
import { EntriesTable } from "~/components/entries-table";
import { PageHeader, Section } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { AccountRef } from "~/components/refs";
import { Notice } from "~/components/ui/notice";
import { accountsById } from "~/lib/accounts";
import { api, path } from "~/lib/api";
import { formatDateTime } from "~/lib/format";
import { routeId } from "~/lib/ids";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import type { Entry, List, Statement } from "~/lib/types";
import type { Route } from "./+types/detail";

export const meta: Route.MetaFunction = () => title("Statement");

export async function clientLoader({ params, request }: Route.ClientLoaderArgs) {
  const id = routeId(params.statementId, "stmt");
  const { cursor } = searchParams(request, []);
  const [statement, entries] = await Promise.all([
    api<Statement>(path`/v1/statements/${id}`, { signal: request.signal }),
    api<List<Entry>>(withQuery("/v1/entries", { statement_id: id, cursor, limit: 100 }), { signal: request.signal }),
  ]);
  const accounts = await accountsById([statement.account_id], request.signal);
  return { statement, entries, accounts };
}

export default function StatementDetail({ loaderData }: Route.ComponentProps) {
  const { statement: s, entries, accounts } = loaderData;
  const exponent = accounts.get(s.account_id)?.currency_exponent ?? 0;
  return (
    <div className="grid gap-8">
      <PageHeader title={s.description || "Statement"} />
      <Details
        items={[
          { term: "Account", value: <AccountRef id={s.account_id} accounts={accounts} /> },
          { term: "Period", value: `${formatDateTime(s.effective_at_lower_bound)} – ${formatDateTime(s.effective_at_upper_bound)}` },
          { term: "Opening balance", value: <Amount value={s.starting_balance.amount} exponent={exponent} currency={s.currency} /> },
          { term: "Closing balance", value: <Amount value={s.ending_balance.amount} exponent={exponent} currency={s.currency} /> },
          { term: "Entries", value: s.entry_count },
          { term: "Created", value: formatDateTime(s.created_at) },
          { term: "ID", value: s.id, mono: true },
        ]}
      />
      <Section title="Entries">
        {entries.data.length === 0 ? <Notice>No entries in this period.</Notice> : <EntriesTable entries={entries.data} accounts={accounts} />}
        <Pagination nextCursor={entries.next_cursor} />
      </Section>
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
