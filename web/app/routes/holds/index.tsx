import { Amount } from "~/components/amount";
import { FilterBar } from "~/components/filter-bar";
import { PageHeader } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { AccountRef } from "~/components/refs";
import { StatusText } from "~/components/status";
import { Input, Select } from "~/components/ui/field";
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
import type { Hold, List } from "~/lib/types";
import type { Route } from "./+types/index";

export const meta: Route.MetaFunction = () => title("Holds");

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const { cursor, filters } = searchParams(request, ["account_id", "status"]);
  const holds = await api<List<Hold>>(
    withQuery("/v1/holds", { account_id: isId(filters.account_id, "acct") ? filters.account_id : "", status: filters.status, cursor, limit: 50 }),
    { signal: request.signal },
  );
  const accounts = await accountsById(holds.data.map((h) => h.account_id), request.signal);
  return { holds, accounts, filters };
}

export default function Holds({ loaderData }: Route.ComponentProps) {
  const { holds, accounts, filters } = loaderData;
  const filtered = Object.values(filters).some(Boolean);
  return (
    <div className="grid gap-6">
      <PageHeader title="Holds" actions={canWrite(useApiKey().role) && <ButtonLink to="/holds/new" variant="primary">New hold</ButtonLink>} />
      <p className="max-w-2xl text-muted">A hold reserves part of an account's available balance until it is captured, voided or expires.</p>
      <FilterBar label="Filter holds" active={filtered}>
        <Select name="status" defaultValue={filters.status} aria-label="Status" className="w-32">
          <option value="">Any status</option>
          <option value="pending">Pending</option>
          <option value="captured">Captured</option>
          <option value="voided">Voided</option>
          <option value="expired">Expired</option>
        </Select>
        <Input name="account_id" defaultValue={filters.account_id} placeholder="Account ID" aria-label="Account ID" className="w-72 font-mono text-xs" />
      </FilterBar>
      {holds.data.length === 0 ? (
        <Notice>{filtered ? "No holds match these filters." : "No holds yet."}</Notice>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Created</Th>
              <Th>Description</Th>
              <Th>Account</Th>
              <Th>Status</Th>
              <Th numeric>Amount</Th>
              <Th>Expires</Th>
            </tr>
          </thead>
          <tbody>
            {holds.data.map((h) => (
              <tr key={h.id}>
                <Td className="whitespace-nowrap">{formatDateTime(h.created_at)}</Td>
                <Td>
                  <TextLink to={`/holds/${h.id}`}>{h.description || "Hold"}</TextLink>
                </Td>
                <Td>
                  <AccountRef id={h.account_id} accounts={accounts} />
                </Td>
                <Td>
                  <StatusText status={h.status} />
                </Td>
                <Td numeric>
                  <Amount value={h.amount} exponent={accounts.get(h.account_id)?.currency_exponent ?? 0} currency={h.currency} />
                </Td>
                <Td className="whitespace-nowrap">{formatDateTime(h.expires_at)}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      <Pagination nextCursor={holds.next_cursor} />
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
