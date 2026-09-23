import { useEffect } from "react";
import { useRevalidator } from "react-router";
import { Details } from "~/components/details";
import { PageHeader, Section } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { IdLink } from "~/components/refs";
import { StatusText } from "~/components/status";
import { Table, Td, Th } from "~/components/ui/table";
import { api, errorMessage, path } from "~/lib/api";
import { formatDateTime } from "~/lib/format";
import { routeId } from "~/lib/ids";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import type { BulkRequest, BulkResult, List } from "~/lib/types";
import type { Route } from "./+types/detail";

export const meta: Route.MetaFunction = () => title("Bulk request");

export async function clientLoader({ params, request }: Route.ClientLoaderArgs) {
  const id = routeId(params.bulkId, "blk");
  const { cursor } = searchParams(request, []);
  const [bulk, results] = await Promise.all([
    api<BulkRequest>(path`/v1/bulk_requests/${id}`, { signal: request.signal }),
    api<List<BulkResult>>(withQuery(path`/v1/bulk_requests/${id}/results`, { cursor, limit: 100 }), { signal: request.signal }),
  ]);
  return { bulk, results };
}

export default function BulkDetail({ loaderData }: Route.ComponentProps) {
  const { bulk, results } = loaderData;
  const revalidator = useRevalidator();
  const running = bulk.status !== "completed";

  // Progress changes on the server, so the page refreshes itself until the request completes.
  useEffect(() => {
    if (!running) return;
    const timer = setInterval(() => {
      if (revalidator.state === "idle") void revalidator.revalidate();
    }, 2000);
    return () => clearInterval(timer);
  }, [running, revalidator]);

  const percent = bulk.total === 0 ? 100 : Math.floor((bulk.processed / bulk.total) * 100);
  return (
    <div className="grid gap-8">
      <PageHeader title="Bulk request" />
      <Section title="Progress">
        <div className="grid gap-2">
          <div className="h-2 border border-line-strong" role="progressbar" aria-valuenow={percent} aria-valuemin={0} aria-valuemax={100}>
            <div className="h-full bg-ink" style={{ width: `${percent}%` }} />
          </div>
          <p className="tabular-nums">
            {bulk.processed.toLocaleString()} of {bulk.total.toLocaleString()} processed: {bulk.succeeded.toLocaleString()} posted,{" "}
            <span className={bulk.failed > 0 ? "text-danger" : ""}>{bulk.failed.toLocaleString()} failed</span>
            {running && <span className="text-muted"> — refreshing</span>}
          </p>
        </div>
      </Section>
      <Details
        items={[
          { term: "Status", value: <StatusText status={bulk.status} /> },
          { term: "ID", value: bulk.id, mono: true },
          { term: "Idempotency key", value: bulk.idempotency_key, mono: true },
          { term: "Created", value: formatDateTime(bulk.created_at) },
          ...(bulk.started_at ? [{ term: "Started", value: formatDateTime(bulk.started_at) }] : []),
          ...(bulk.completed_at ? [{ term: "Completed", value: formatDateTime(bulk.completed_at) }] : []),
        ]}
      />
      <Section title="Results">
        <Table>
          <thead>
            <tr>
              <Th numeric>#</Th>
              <Th>Status</Th>
              <Th>Transaction or error</Th>
            </tr>
          </thead>
          <tbody>
            {results.data.map((r) => (
              <tr key={r.index}>
                <Td numeric>{r.index}</Td>
                <Td>
                  <StatusText status={r.status} />
                </Td>
                <Td>
                  {r.transaction_id && <IdLink id={r.transaction_id} />}
                  {r.error && (
                    <span className="text-danger">{errorMessage(r.error.code, r.error.detail) ?? r.error.code}</span>
                  )}
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
        <Pagination nextCursor={results.next_cursor} />
      </Section>
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
