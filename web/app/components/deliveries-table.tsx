import { useFetcher } from "react-router";
import { formatDateTime } from "~/lib/format";
import type { WebhookDelivery } from "~/lib/types";
import { IdLink } from "./refs";
import { StatusText } from "./status";
import { Button } from "./ui/button";
import { Table, Td, Th } from "./ui/table";

/** Webhook deliveries with their last attempt; pages that show it handle the "retry" intent in their action. */
export function DeliveriesTable({ deliveries, canRetry }: { deliveries: WebhookDelivery[]; canRetry: boolean }) {
  return (
    <Table>
      <thead>
        <tr>
          <Th>Created</Th>
          <Th>Event</Th>
          <Th>Status</Th>
          <Th numeric>Attempts</Th>
          <Th>Last attempt</Th>
          <Th>Next attempt</Th>
          {canRetry && <Th />}
        </tr>
      </thead>
      <tbody>
        {deliveries.map((d) => (
          <tr key={d.id}>
            <Td className="whitespace-nowrap">{formatDateTime(d.created_at)}</Td>
            <Td>
              <span className="font-mono text-xs">{d.event_type}</span> <IdLink id={d.event_id} />
            </Td>
            <Td>
              <StatusText status={d.status} />
            </Td>
            <Td numeric>{d.attempts}</Td>
            <Td>
              {d.last_attempt_at ? (
                <>
                  {formatDateTime(d.last_attempt_at)}
                  {d.last_status_code !== null && <span className="ml-2 tabular-nums">HTTP {d.last_status_code}</span>}
                  {d.last_error && <span className="block text-danger">{d.last_error}</span>}
                </>
              ) : (
                <span className="text-muted">Not yet</span>
              )}
            </Td>
            <Td className="whitespace-nowrap">{d.next_attempt_at ? formatDateTime(d.next_attempt_at) : <span className="text-muted">—</span>}</Td>
            {canRetry && <Td className="w-0">{d.status !== "succeeded" && <RetryButton id={d.id} />}</Td>}
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

function RetryButton({ id }: { id: string }) {
  const fetcher = useFetcher({ key: "delivery-retry" });
  return (
    <fetcher.Form method="post">
      <input type="hidden" name="intent" value="retry" />
      <input type="hidden" name="delivery_id" value={id} />
      <Button type="submit" className="h-7" disabled={fetcher.state !== "idle"}>
        Retry now
      </Button>
    </fetcher.Form>
  );
}
