import { redirect, useFetcher } from "react-router";
import { AccountSelect } from "~/components/account-select";
import { Amount } from "~/components/amount";
import { BalanceTable } from "~/components/balance-table";
import { ActionError, ConfirmAction } from "~/components/confirm-action";
import { Details, MetadataValue } from "~/components/details";
import { DateRangeFields } from "~/components/date-range-fields";
import { FilterBar } from "~/components/filter-bar";
import { PageHeader, Section } from "~/components/page";
import { AccountRef } from "~/components/refs";
import { Button } from "~/components/ui/button";
import { Select } from "~/components/ui/field";
import { ButtonLink, TextLink } from "~/components/ui/link";
import { Notice } from "~/components/ui/notice";
import { Table, Td, Th } from "~/components/ui/table";
import { ledgerAccounts } from "~/lib/accounts";
import { api, fetchAll, path } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { formError, text } from "~/lib/forms";
import { isId, routeId } from "~/lib/ids";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import { canWrite } from "~/lib/session";
import { toApiTime } from "~/lib/time";
import type { Account, Category, Currency, Ledger } from "~/lib/types";
import type { Route } from "./+types/detail";

export const meta: Route.MetaFunction = ({ loaderData }) => title(loaderData?.category.name ?? "Category");

export async function clientLoader({ params, request }: Route.ClientLoaderArgs) {
  const id = routeId(params.categoryId, "cat");
  const { filters } = searchParams(request, ["from", "until"]);
  const signal = request.signal;
  const category = await api<Category>(
    withQuery(path`/v1/account_categories/${id}`, {
      effective_at_lower_bound: toApiTime(filters.from),
      effective_at_upper_bound: toApiTime(filters.until),
    }),
    { signal },
  );
  const [ledger, currency, children, members, allCategories, ledgerAccountList] = await Promise.all([
    api<Ledger>(path`/v1/ledgers/${category.ledger_id}`, { signal }),
    api<Currency>(path`/v1/currencies/${category.currency}`, { signal }),
    fetchAll<Category>(withQuery("/v1/account_categories", { parent_id: id }), { signal }),
    fetchAll<Account>(withQuery("/v1/accounts", { category_id: id }), { signal }),
    fetchAll<Category>(withQuery("/v1/account_categories", { ledger_id: category.ledger_id }), { signal }),
    ledgerAccounts(category.ledger_id, signal),
  ]);
  // The accounts list includes accounts reached through nested categories; only direct members can be removed here.
  const direct = new Set(
    (
      await Promise.all(
        members.map(async (account) => {
          const of = await fetchAll<Category>(withQuery("/v1/account_categories", { account_id: account.id }), { signal });
          return of.some((c) => c.id === id) ? account.id : null;
        }),
      )
    ).filter((accountId): accountId is string => accountId !== null),
  );
  const memberIds = new Set(members.map((m) => m.id));
  const childIds = new Set(children.map((c) => c.id));
  return {
    category,
    ledger,
    exponent: currency.exponent,
    children,
    members,
    direct: [...direct],
    candidates: ledgerAccountList.filter((a) => a.currency === category.currency && !memberIds.has(a.id)),
    nestable: allCategories.filter((c) => c.currency === category.currency && c.id !== id && !childIds.has(c.id)),
    filters,
  };
}

const fetcherKey = "category-action";

export async function clientAction({ params, request }: Route.ClientActionArgs) {
  const id = routeId(params.categoryId, "cat");
  const form = await request.formData();
  const intent = text(form, "intent");
  const member = text(form, "member");
  try {
    switch (intent) {
      case "add-account":
      case "remove-account":
        if (!isId(member, "acct")) return { error: "Choose an account." };
        await api(path`/v1/account_categories/${id}/accounts/${member}`, { method: intent === "add-account" ? "PUT" : "DELETE", signal: request.signal });
        return null;
      case "nest":
      case "unnest":
        if (!isId(member, "cat")) return { error: "Choose a category." };
        await api(path`/v1/account_categories/${id}/categories/${member}`, { method: intent === "nest" ? "PUT" : "DELETE", signal: request.signal });
        return null;
      case "delete":
        await api(path`/v1/account_categories/${id}`, { method: "DELETE", signal: request.signal });
        throw redirect("/categories");
    }
  } catch (err) {
    if (err instanceof Response) throw err;
    return formError(err);
  }
  return { error: "Unknown action." };
}

export default function CategoryDetail({ loaderData }: Route.ComponentProps) {
  const { category: c, ledger, exponent, children, members, direct, candidates, nestable, filters } = loaderData;
  const writable = canWrite(useApiKey().role);
  const directMembers = new Set(direct);
  const windowed = Boolean(filters.from || filters.until);

  return (
    <div className="grid gap-8">
      <PageHeader
        title={c.name}
        actions={
          writable && (
            <>
              <ButtonLink to={`/categories/${c.id}/edit`}>Edit</ButtonLink>
              <ConfirmAction fetcherKey={fetcherKey} intent="delete" label="Delete" title={`Delete ${c.name}?`} confirmLabel="Delete category" variant="danger">
                <p>The category and its nesting are removed. Accounts and their balances are not touched.</p>
              </ConfirmAction>
            </>
          )
        }
      />
      <ActionError fetcherKey={fetcherKey} />

      <Section title={windowed ? "Balances for the chosen period" : "Balances"}>
        <FilterBar label="Balance period" active={windowed}>
          <DateRangeFields from={filters.from} until={filters.until} />
        </FilterBar>
        <BalanceTable balances={c.balances} exponent={exponent} currency={c.currency} />
        <p className="text-muted">Every account reachable through this category counts once, on the category's normal side ({c.normal_side}).</p>
      </Section>

      <Section title="Nested categories">
        {children.length === 0 ? (
          <Notice>None.</Notice>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>Name</Th>
                <Th numeric>Posted</Th>
                {writable && <Th />}
              </tr>
            </thead>
            <tbody>
              {children.map((child) => (
                <tr key={child.id}>
                  <Td>
                    <TextLink to={`/categories/${child.id}`}>{child.name}</TextLink>
                  </Td>
                  <Td numeric>
                    <Amount value={child.balances.posted.amount} exponent={exponent} />
                  </Td>
                  {writable && (
                    <Td className="w-0 text-right">
                      <MemberAction intent="unnest" member={child.id} label="Unnest" />
                    </Td>
                  )}
                </tr>
              ))}
            </tbody>
          </Table>
        )}
        {writable && nestable.length > 0 && (
          <AddMember intent="nest" label="Nest category">
            <Select name="member" aria-label="Category to nest" required className="w-72">
              <option value="">Choose a category</option>
              {nestable.map((n) => (
                <option key={n.id} value={n.id}>
                  {n.name}
                </option>
              ))}
            </Select>
          </AddMember>
        )}
      </Section>

      <Section title="Accounts">
        <p className="text-muted">Accounts in this category or any category nested under it.</p>
        {members.length === 0 ? (
          <Notice>None.</Notice>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>Code</Th>
                <Th>Name</Th>
                <Th numeric>Posted</Th>
                <Th>Membership</Th>
                {writable && <Th />}
              </tr>
            </thead>
            <tbody>
              {members.map((a) => (
                <tr key={a.id}>
                  <Td>
                    <AccountRef id={a.id} accounts={new Map([[a.id, a]])} />
                  </Td>
                  <Td>{a.name}</Td>
                  <Td numeric>
                    <Amount value={a.balances.posted.amount} exponent={a.currency_exponent} />
                  </Td>
                  <Td className="text-muted">{directMembers.has(a.id) ? "Direct" : "Through a nested category"}</Td>
                  {writable && <Td className="w-0 text-right">{directMembers.has(a.id) && <MemberAction intent="remove-account" member={a.id} label="Remove" />}</Td>}
                </tr>
              ))}
            </tbody>
          </Table>
        )}
        {writable && candidates.length > 0 && (
          <AddMember intent="add-account" label="Add account">
            <AccountSelect name="member" accounts={candidates} allowInactive aria-label="Account to add" required className="w-96" />
          </AddMember>
        )}
      </Section>

      <Details
        items={[
          { term: "Ledger", value: <TextLink to={`/ledgers/${ledger.id}`}>{ledger.name}</TextLink> },
          { term: "Currency", value: c.currency },
          { term: "Normal side", value: c.normal_side },
          { term: "Description", value: c.description || <span className="text-muted">None</span> },
          { term: "ID", value: c.id, mono: true },
          { term: "Metadata", value: <MetadataValue metadata={c.metadata} /> },
        ]}
      />
    </div>
  );
}

function AddMember({ intent, label, children }: { intent: string; label: string; children: React.ReactNode }) {
  const fetcher = useFetcher({ key: fetcherKey });
  return (
    <fetcher.Form method="post" className="flex items-center gap-2">
      <input type="hidden" name="intent" value={intent} />
      {children}
      <Button type="submit" disabled={fetcher.state !== "idle"}>
        {label}
      </Button>
    </fetcher.Form>
  );
}

function MemberAction({ intent, member, label }: { intent: string; member: string; label: string }) {
  const fetcher = useFetcher({ key: fetcherKey });
  return (
    <fetcher.Form method="post">
      <input type="hidden" name="intent" value={intent} />
      <input type="hidden" name="member" value={member} />
      <Button type="submit" disabled={fetcher.state !== "idle"} className="h-7">
        {label}
      </Button>
    </fetcher.Form>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
