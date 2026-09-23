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
import { isId } from "~/lib/ids";
import { title } from "~/lib/meta";
import { monitorFields, monitorOperators } from "~/lib/monitors";
import { searchParams, withQuery } from "~/lib/query";
import { canWrite } from "~/lib/session";
import type { BalanceMonitor, List } from "~/lib/types";
import type { Route } from "./+types/index";

export const meta: Route.MetaFunction = () => title("Balance monitors");

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const { cursor, filters } = searchParams(request, ["account_id"]);
  const monitors = await api<List<BalanceMonitor>>(
    withQuery("/v1/balance_monitors", { account_id: isId(filters.account_id, "acct") ? filters.account_id : "", cursor, limit: 50 }),
    { signal: request.signal },
  );
  const accounts = await accountsById(monitors.data.map((m) => m.account_id), request.signal);
  return { monitors, accounts, filters };
}

export default function Monitors({ loaderData }: Route.ComponentProps) {
  const { monitors, accounts, filters } = loaderData;
  return (
    <div className="grid gap-6">
      <PageHeader title="Balance monitors" actions={canWrite(useApiKey().role) && <ButtonLink to="/monitors/new" variant="primary">New monitor</ButtonLink>} />
      <p className="max-w-2xl text-muted">
        A monitor emits a <code className="font-mono">balance_monitor.triggered</code> event each time an account's balance moves into its condition.
      </p>
      <FilterBar label="Filter monitors" active={Boolean(filters.account_id)}>
        <Input name="account_id" defaultValue={filters.account_id} placeholder="Account ID" aria-label="Account ID" className="w-72 font-mono text-xs" />
      </FilterBar>
      {monitors.data.length === 0 ? (
        <Notice>No monitors yet.</Notice>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Description</Th>
              <Th>Account</Th>
              <Th>Condition</Th>
              <Th>Now</Th>
            </tr>
          </thead>
          <tbody>
            {monitors.data.map((m) => {
              const account = accounts.get(m.account_id);
              return (
                <tr key={m.id}>
                  <Td>
                    <TextLink to={`/monitors/${m.id}`}>{m.description || "Monitor"}</TextLink>
                  </Td>
                  <Td>
                    <AccountRef id={m.account_id} accounts={accounts} />
                  </Td>
                  <Td>
                    {monitorFields[m.alert_condition.field]} {monitorOperators[m.alert_condition.operator]}{" "}
                    <Amount value={m.alert_condition.value} exponent={account?.currency_exponent ?? 0} currency={account?.currency} />
                  </Td>
                  <Td>{m.triggered ? <span className="text-warning">In condition</span> : <span className="text-muted">Not in condition</span>}</Td>
                </tr>
              );
            })}
          </tbody>
        </Table>
      )}
      <Pagination nextCursor={monitors.next_cursor} />
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
