import { FilterBar } from "~/components/filter-bar";
import { PageHeader } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { IdLink } from "~/components/refs";
import { StatusText } from "~/components/status";
import { Select } from "~/components/ui/field";
import { ButtonLink, TextLink } from "~/components/ui/link";
import { Notice } from "~/components/ui/notice";
import { Table, Td, Th } from "~/components/ui/table";
import { api } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { formatDateTime } from "~/lib/format";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import { canWrite } from "~/lib/session";
import type { List, ScheduledTransaction } from "~/lib/types";
import type { Route } from "./+types/index";

export const meta: Route.MetaFunction = () => title("Scheduled transactions");

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const { cursor, filters } = searchParams(request, ["status"]);
  const schedules = await api<List<ScheduledTransaction>>(withQuery("/v1/scheduled_transactions", { status: filters.status, cursor, limit: 50 }), {
    signal: request.signal,
  });
  return { schedules, filters };
}

export default function Scheduled({ loaderData }: Route.ComponentProps) {
  const { schedules, filters } = loaderData;
  return (
    <div className="grid gap-6">
      <PageHeader
        title="Scheduled transactions"
        actions={canWrite(useApiKey().role) && <ButtonLink to="/scheduled/new" variant="primary">Schedule transaction</ButtonLink>}
      />
      <FilterBar label="Filter scheduled transactions" active={Boolean(filters.status)}>
        <Select name="status" defaultValue={filters.status} aria-label="Status" className="w-36">
          <option value="">Any status</option>
          <option value="scheduled">Scheduled</option>
          <option value="executed">Executed</option>
          <option value="failed">Failed</option>
          <option value="canceled">Canceled</option>
        </Select>
      </FilterBar>
      {schedules.data.length === 0 ? (
        <Notice>{filters.status ? "No scheduled transactions match this filter." : "Nothing is scheduled."}</Notice>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Posts at</Th>
              <Th>Description</Th>
              <Th>Status</Th>
              <Th>Transaction</Th>
            </tr>
          </thead>
          <tbody>
            {schedules.data.map((s) => (
              <tr key={s.id}>
                <Td className="whitespace-nowrap">{formatDateTime(s.execute_at)}</Td>
                <Td>
                  <TextLink to={`/scheduled/${s.id}`}>{s.description || "Scheduled transaction"}</TextLink>
                </Td>
                <Td>
                  <StatusText status={s.status} />
                </Td>
                <Td>{s.transaction_id ? <IdLink id={s.transaction_id} /> : <span className="text-muted">—</span>}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      <Pagination nextCursor={schedules.next_cursor} />
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
