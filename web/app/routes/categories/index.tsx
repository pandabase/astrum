import { Amount } from "~/components/amount";
import { FilterBar } from "~/components/filter-bar";
import { PageHeader } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { Select } from "~/components/ui/field";
import { ButtonLink, TextLink } from "~/components/ui/link";
import { Notice } from "~/components/ui/notice";
import { Table, Td, Th } from "~/components/ui/table";
import { api, fetchAll } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { isId } from "~/lib/ids";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import { canWrite } from "~/lib/session";
import type { Category, Currency, Ledger, List } from "~/lib/types";
import type { Route } from "./+types/index";

export const meta: Route.MetaFunction = () => title("Categories");

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const { cursor, filters } = searchParams(request, ["ledger_id"]);
  const [ledgers, categories, currencies] = await Promise.all([
    fetchAll<Ledger>("/v1/ledgers", { signal: request.signal }),
    api<List<Category>>(withQuery("/v1/account_categories", { ledger_id: isId(filters.ledger_id, "ldg") ? filters.ledger_id : "", cursor, limit: 50 }), {
      signal: request.signal,
    }),
    fetchAll<Currency>("/v1/currencies", { signal: request.signal }),
  ]);
  return { ledgers, categories, exponents: Object.fromEntries(currencies.map((c) => [c.code, c.exponent])), filters };
}

export default function Categories({ loaderData }: Route.ComponentProps) {
  const { ledgers, categories, exponents, filters } = loaderData;
  const ledgerNames = new Map(ledgers.map((l) => [l.id, l.name]));
  return (
    <div className="grid gap-6">
      <PageHeader
        title="Categories"
        actions={canWrite(useApiKey().role) && <ButtonLink to={withQuery("/categories/new", { ledger_id: filters.ledger_id })} variant="primary">New category</ButtonLink>}
      />
      <p className="max-w-2xl text-muted">
        Categories group accounts and other categories into a chart of accounts, such as Assets or Customer balances, and roll up their balances.
      </p>
      <FilterBar label="Filter categories" active={Boolean(filters.ledger_id)}>
        <Select name="ledger_id" defaultValue={filters.ledger_id} aria-label="Ledger" className="w-44">
          <option value="">Any ledger</option>
          {ledgers.map((l) => (
            <option key={l.id} value={l.id}>
              {l.name}
            </option>
          ))}
        </Select>
      </FilterBar>
      {categories.data.length === 0 ? (
        <Notice>No categories yet.</Notice>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Name</Th>
              <Th>Ledger</Th>
              <Th>Normal side</Th>
              <Th numeric>Posted</Th>
              <Th numeric>Available</Th>
              <Th>Currency</Th>
            </tr>
          </thead>
          <tbody>
            {categories.data.map((c) => (
              <tr key={c.id}>
                <Td>
                  <TextLink to={`/categories/${c.id}`}>{c.name}</TextLink>
                </Td>
                <Td>{ledgerNames.get(c.ledger_id)}</Td>
                <Td>{c.normal_side}</Td>
                <Td numeric>
                  <Amount value={c.balances.posted.amount} exponent={exponents[c.currency] ?? 0} />
                </Td>
                <Td numeric>
                  <Amount value={c.balances.available.amount} exponent={exponents[c.currency] ?? 0} />
                </Td>
                <Td className="text-muted">{c.currency}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      <Pagination nextCursor={categories.next_cursor} />
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
