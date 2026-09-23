import { FilterBar } from "~/components/filter-bar";
import { PageHeader } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { IdLink } from "~/components/refs";
import { Select } from "~/components/ui/field";
import { TextLink } from "~/components/ui/link";
import { Notice } from "~/components/ui/notice";
import { Table, Td, Th } from "~/components/ui/table";
import { api } from "~/lib/api";
import { eventTypes } from "~/lib/event-types";
import { formatDateTime } from "~/lib/format";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import type { Event, List } from "~/lib/types";
import type { Route } from "./+types/index";

export const meta: Route.MetaFunction = () => title("Events");

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const { cursor, filters } = searchParams(request, ["type"]);
  const events = await api<List<Event>>(withQuery("/v1/events", { type: filters.type, cursor, limit: 50 }), { signal: request.signal });
  return { events, filters };
}

/** The resource an event is about, taken from its data. */
function subject(event: Event): string | null {
  const id = event.data?.id;
  return typeof id === "string" ? id : null;
}

export default function Events({ loaderData }: Route.ComponentProps) {
  const { events, filters } = loaderData;
  return (
    <div className="grid gap-6">
      <PageHeader title="Events" />
      <p className="max-w-2xl text-muted">Every change, recorded in the same database transaction as the change itself. Newest first.</p>
      <FilterBar label="Filter events" active={Boolean(filters.type)}>
        <Select name="type" defaultValue={filters.type} aria-label="Type" className="w-64">
          <option value="">Any type</option>
          {eventTypes.map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </Select>
      </FilterBar>
      {events.data.length === 0 ? (
        <Notice>No events.</Notice>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Created</Th>
              <Th>Type</Th>
              <Th>About</Th>
              <Th>ID</Th>
            </tr>
          </thead>
          <tbody>
            {events.data.map((e) => {
              const about = subject(e);
              return (
                <tr key={e.id}>
                  <Td className="whitespace-nowrap">{formatDateTime(e.created_at)}</Td>
                  <Td>
                    <TextLink to={`/events/${e.id}`} className="font-mono text-xs">
                      {e.type}
                    </TextLink>
                  </Td>
                  <Td>{about ? <IdLink id={about} /> : <span className="text-muted">—</span>}</Td>
                  <Td className="font-mono text-xs">{e.id}</Td>
                </tr>
              );
            })}
          </tbody>
        </Table>
      )}
      <Pagination nextCursor={events.next_cursor} />
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
