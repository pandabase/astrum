import { Amount } from "~/components/amount";
import { FilterBar } from "~/components/filter-bar";
import { PageHeader } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { AccountRef } from "~/components/refs";
import { Input } from "~/components/ui/field";
import { ButtonLink, TextLink } from "~/components/ui/link";
import { Notice } from "~/components/ui/notice";
import { Table, Td, Th } from "~/components/ui/table";
import { accountsById } from "~/lib/accounts";
import { api } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { formatDateTime } from "~/lib/format";
import { isId } from "~/lib/ids";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import { canWrite } from "~/lib/session";
import type { List, Settlement } from "~/lib/types";
import type { Route } from "./+types/index";

export const meta: Route.MetaFunction = () => title("Settlements");

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const { cursor, filters } = searchParams(request, ["account_id"]);
  const settlements = await api<List<Settlement>>(
    withQuery("/v1/settlements", { account_id: isId(filters.account_id, "acct") ? filters.account_id : "", cursor, limit: 50 }),
    { signal: request.signal },
  );
  const accounts = await accountsById(settlements.data.flatMap((s) => [s.settled_account_id, s.contra_account_id]), request.signal);
  return { settlements, accounts, filters };
}

export default function Settlements({ loaderData }: Route.ComponentProps) {
  const { settlements, accounts, filters } = loaderData;
  return (
    <div className="grid gap-6">
      <PageHeader title="Settlements" actions={canWrite(useApiKey().role) && <ButtonLink to="/settlements/new" variant="primary">New settlement</ButtonLink>} />
      <p className="max-w-2xl text-muted">
        A settlement moves everything not yet settled on one account into a contra account in a single transaction, and marks each entry it
        covered so it is never settled twice.
      </p>
      <FilterBar label="Filter settlements" active={Boolean(filters.account_id)}>
        <Input name="account_id" defaultValue={filters.account_id} placeholder="Settled account ID" aria-label="Settled account ID" className="w-72 font-mono text-xs" />
      </FilterBar>
      {settlements.data.length === 0 ? (
        <Notice>No settlements yet.</Notice>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Created</Th>
              <Th>Description</Th>
              <Th>Settled</Th>
              <Th>Into</Th>
              <Th numeric>Amount</Th>
              <Th numeric>Entries</Th>
            </tr>
          </thead>
          <tbody>
            {settlements.data.map((s) => (
              <tr key={s.id}>
                <Td className="whitespace-nowrap">{formatDateTime(s.created_at)}</Td>
                <Td>
                  <TextLink to={`/settlements/${s.id}`}>{s.description || "Settlement"}</TextLink>
                </Td>
                <Td>
                  <AccountRef id={s.settled_account_id} accounts={accounts} />
                </Td>
                <Td>
                  <AccountRef id={s.contra_account_id} accounts={accounts} />
                </Td>
                <Td numeric>
                  <Amount value={s.amount} exponent={accounts.get(s.settled_account_id)?.currency_exponent ?? 0} currency={s.currency} />
                </Td>
                <Td numeric>{s.entry_count}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      <Pagination nextCursor={settlements.next_cursor} />
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
