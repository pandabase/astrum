import { FilterBar } from "~/components/filter-bar";
import { StatusText } from "~/components/status";
import { Amount } from "~/components/amount";
import { Details, MetadataValue } from "~/components/details";
import { PageHeader, Section } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { Input, Select } from "~/components/ui/field";
import { ButtonLink, TextLink } from "~/components/ui/link";
import { Notice } from "~/components/ui/notice";
import { Table, Td, Th } from "~/components/ui/table";
import { api, path } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { formatDateTime } from "~/lib/format";
import { routeId } from "~/lib/ids";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import { canWrite } from "~/lib/session";
import type { Account, Ledger, List } from "~/lib/types";
import type { Route } from "./+types/detail";

export const meta: Route.MetaFunction = ({ loaderData }) => title(loaderData?.ledger.name ?? "Ledger");

const filterNames = ["code", "currency", "status"] as const;

export async function clientLoader({ params, request }: Route.ClientLoaderArgs) {
  const ledgerId = routeId(params.ledgerId, "ldg");
  const { cursor, filters } = searchParams(request, filterNames);
  const [ledger, accounts] = await Promise.all([
    api<Ledger>(path`/v1/ledgers/${ledgerId}`, { signal: request.signal }),
    api<List<Account>>(
      withQuery("/v1/accounts", { ledger_id: ledgerId, ...filters, currency: filters.currency.toUpperCase(), cursor, limit: 50 }),
      { signal: request.signal },
    ),
  ]);
  return { ledger, accounts, filters };
}

export default function LedgerDetail({ loaderData }: Route.ComponentProps) {
  const { ledger, accounts, filters } = loaderData;
  const writable = canWrite(useApiKey().role);
  const filtered = Object.values(filters).some(Boolean);

  return (
    <div className="grid gap-8">
      <PageHeader
        title={ledger.name}
        actions={
          <>
            <ButtonLink to={withQuery("/transactions", { ledger_id: ledger.id })}>Transactions</ButtonLink>
            <ButtonLink to={withQuery("/categories", { ledger_id: ledger.id })}>Categories</ButtonLink>
            {writable && <ButtonLink to={`/ledgers/${ledger.id}/edit`}>Edit</ButtonLink>}
          </>
        }
      />

      <Details
        items={[
          { term: "ID", value: ledger.id, mono: true },
          { term: "Description", value: ledger.description || <span className="text-muted">None</span> },
          { term: "Created", value: formatDateTime(ledger.created_at) },
          { term: "Metadata", value: <MetadataValue metadata={ledger.metadata} /> },
        ]}
      />

      <Section title="Accounts">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <FilterBar label="Filter accounts" active={filtered}>
            <Input name="code" placeholder="Code" defaultValue={filters.code} aria-label="Code" className="w-40" />
            <Input name="currency" placeholder="Currency" defaultValue={filters.currency} aria-label="Currency" className="w-28 uppercase placeholder:normal-case" />
            <Select name="status" defaultValue={filters.status} aria-label="Status" className="w-32">
              <option value="">Any status</option>
              <option value="open">Open</option>
              <option value="frozen">Frozen</option>
              <option value="closed">Closed</option>
            </Select>
          </FilterBar>
          {writable && (
            <ButtonLink to={`/ledgers/${ledger.id}/accounts/new`} variant="primary">
              New account
            </ButtonLink>
          )}
        </div>

        {accounts.data.length === 0 ? (
          <Notice>{filtered ? "No accounts match these filters." : "No accounts in this ledger yet."}</Notice>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>Code</Th>
                <Th>Name</Th>
                <Th>Normal side</Th>
                <Th>Status</Th>
                <Th numeric>Posted</Th>
                <Th numeric>Available</Th>
                <Th>Currency</Th>
              </tr>
            </thead>
            <tbody>
              {accounts.data.map((account) => (
                <tr key={account.id}>
                  <Td>
                    <TextLink to={`/accounts/${account.id}`} className="font-mono text-xs">
                      {account.code}
                    </TextLink>
                  </Td>
                  <Td>{account.name}</Td>
                  <Td>{account.normal_side}</Td>
                  <Td>
                    <StatusText status={account.status} />
                  </Td>
                  <Td numeric>
                    <Amount value={account.balances.posted.amount} exponent={account.currency_exponent} />
                  </Td>
                  <Td numeric>
                    <Amount value={account.balances.available.amount} exponent={account.currency_exponent} />
                  </Td>
                  <Td className="text-muted">{account.currency}</Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
        <Pagination nextCursor={accounts.next_cursor} />
      </Section>
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
