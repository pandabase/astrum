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
import type { List, Statement } from "~/lib/types";
import type { Route } from "./+types/index";

export const meta: Route.MetaFunction = () => title("Statements");

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const { cursor, filters } = searchParams(request, ["account_id"]);
  const statements = await api<List<Statement>>(
    withQuery("/v1/statements", { account_id: isId(filters.account_id, "acct") ? filters.account_id : "", cursor, limit: 50 }),
    { signal: request.signal },
  );
  const accounts = await accountsById(statements.data.map((s) => s.account_id), request.signal);
  return { statements, accounts, filters };
}

export default function Statements({ loaderData }: Route.ComponentProps) {
  const { statements, accounts, filters } = loaderData;
  return (
    <div className="grid gap-6">
      <PageHeader title="Statements" actions={canWrite(useApiKey().role) && <ButtonLink to="/statements/new" variant="primary">New statement</ButtonLink>} />
      <p className="max-w-2xl text-muted">A statement freezes an account's opening balance, closing balance and entries for a period. It never changes afterwards.</p>
      <FilterBar label="Filter statements" active={Boolean(filters.account_id)}>
        <Input name="account_id" defaultValue={filters.account_id} placeholder="Account ID" aria-label="Account ID" className="w-72 font-mono text-xs" />
      </FilterBar>
      {statements.data.length === 0 ? (
        <Notice>No statements yet.</Notice>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Period</Th>
              <Th>Account</Th>
              <Th>Description</Th>
              <Th numeric>Opening</Th>
              <Th numeric>Closing</Th>
              <Th numeric>Entries</Th>
            </tr>
          </thead>
          <tbody>
            {statements.data.map((s) => {
              const exponent = accounts.get(s.account_id)?.currency_exponent ?? 0;
              return (
                <tr key={s.id}>
                  <Td className="whitespace-nowrap">
                    <TextLink to={`/statements/${s.id}`}>
                      {formatDateTime(s.effective_at_lower_bound)} – {formatDateTime(s.effective_at_upper_bound)}
                    </TextLink>
                  </Td>
                  <Td>
                    <AccountRef id={s.account_id} accounts={accounts} />
                  </Td>
                  <Td className="text-muted">{s.description}</Td>
                  <Td numeric>
                    <Amount value={s.starting_balance.amount} exponent={exponent} />
                  </Td>
                  <Td numeric>
                    <Amount value={s.ending_balance.amount} exponent={exponent} currency={s.currency} />
                  </Td>
                  <Td numeric>{s.entry_count}</Td>
                </tr>
              );
            })}
          </tbody>
        </Table>
      )}
      <Pagination nextCursor={statements.next_cursor} />
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
