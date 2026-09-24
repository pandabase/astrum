import { Form, useRevalidator } from "react-router";
import { DateRangeFields } from "~/components/date-range-fields";
import { FilterBar } from "~/components/filter-bar";
import { LifecycleFlow } from "~/components/lifecycle-flow";
import { PageHeader } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { Button } from "~/components/ui/button";
import { Input, Select } from "~/components/ui/field";
import { Notice } from "~/components/ui/notice";
import { api, fetchAll, path } from "~/lib/api";
import { isId, routeId } from "~/lib/ids";
import { loadLifecycle } from "~/lib/lifecycle";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import { toApiTime } from "~/lib/time";
import type { Ledger, List, Transaction } from "~/lib/types";
import type { Route } from "./+types/lifecycle";

export const meta: Route.MetaFunction = () => title("Lifecycle");

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const { cursor, filters } = searchParams(request, [
    "ledger_id",
    "account_id",
    "from",
    "until",
  ]);

  const selected = new URL(request.url).searchParams.get("transaction_id");

  if (selected) routeId(selected, "txn");
  if (filters.ledger_id) routeId(filters.ledger_id, "ldg");
  if (filters.account_id) routeId(filters.account_id, "acct");

  const [ledgers, transactions] = await Promise.all([
    fetchAll<Ledger>("/v1/ledgers", { signal: request.signal }),
    api<List<Transaction>>(
      withQuery("/v1/transactions", {
        ledger_id: isId(filters.ledger_id, "ldg") ? filters.ledger_id : "",
        account_id: isId(filters.account_id, "acct") ? filters.account_id : "",
        effective_at_lower_bound: toApiTime(filters.from),
        effective_at_upper_bound: toApiTime(filters.until),
        cursor,
        limit: 20,
      }),
      { signal: request.signal },
    ),
  ]);

  const transaction = selected
    ? (transactions.data.find((t) => t.id === selected) ??
      (await api<Transaction>(path`/v1/transactions/${selected}`, {
        signal: request.signal,
      })))
    : transactions.data[0];
    
  return {
    ledgers,
    transactions,
    filters,
    cursor,
    lifecycle: transaction
      ? await loadLifecycle(transaction, request.signal)
      : null,
  };
}

export default function Lifecycle({ loaderData }: Route.ComponentProps) {
  const { ledgers, transactions, filters, cursor, lifecycle } = loaderData;
  const revalidator = useRevalidator();
  const selected = lifecycle?.transactions.find(
    (t) => t.id === lifecycle.focus,
  );
  const choices =
    selected && !transactions.data.some((t) => t.id === selected.id)
      ? [selected, ...transactions.data]
      : transactions.data;

  return (
    <div className="grid gap-6">
      <PageHeader
        title="Lifecycle"
        actions={
          <Button
            disabled={revalidator.state !== "idle"}
            onClick={() => revalidator.revalidate()}
          >
            {revalidator.state === "idle" ? "Refresh" : "Refreshing…"}
          </Button>
        }
      />
      <FilterBar
        label="Filter lifecycle transactions"
        active={Object.values(filters).some(Boolean)}
      >
        <label className="grid gap-1 text-xs text-muted">
          Ledger
          <Select
            name="ledger_id"
            defaultValue={filters.ledger_id}
            className="w-48"
          >
            <option value="">All ledgers</option>
            {ledgers.map((ledger) => (
              <option key={ledger.id} value={ledger.id}>
                {ledger.name}
              </option>
            ))}
          </Select>
        </label>
        <label className="grid gap-1 text-xs text-muted">
          Account ID
          <Input
            name="account_id"
            defaultValue={filters.account_id}
            placeholder="acct_…"
            className="w-64 font-mono text-xs"
          />
        </label>
        <DateRangeFields from={filters.from} until={filters.until} />
      </FilterBar>
      {lifecycle ? (
        <>
          <Form
            key={lifecycle.focus}
            className="flex flex-wrap items-end gap-2"
            aria-label="Choose a lifecycle"
          >
            {Object.entries(filters).map(
              ([name, value]) =>
                value && (
                  <input key={name} type="hidden" name={name} value={value} />
                ),
            )}
            {cursor && <input type="hidden" name="cursor" value={cursor} />}
            <label className="grid min-w-0 gap-1 text-xs text-muted">
              Transaction
              <Select
                name="transaction_id"
                defaultValue={lifecycle.focus}
                className="max-w-full sm:w-[32rem]"
              >
                {choices.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.description || t.external_id || t.id} · {t.status} ·{" "}
                    {t.id.slice(-6)}
                  </option>
                ))}
              </Select>
            </label>
            <Button type="submit">Show flow</Button>
          </Form>
          <div aria-busy={revalidator.state !== "idle"}>
            <LifecycleFlow key={lifecycle.focus} data={lifecycle} />
          </div>
        </>
      ) : (
        <Notice>
          No matching transactions.
        </Notice>
      )}
      <Pagination nextCursor={transactions.next_cursor} />
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
