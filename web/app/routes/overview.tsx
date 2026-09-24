import { LifecycleFlow } from "~/components/lifecycle-flow";
import { loadLifecycle } from "~/lib/lifecycle";
import { withQuery } from "~/lib/query";
import { motion, useReducedMotion } from "motion/react";
import { Link } from "react-router";
import { Details } from "~/components/details";
import { PageHeader, Section } from "~/components/page";
import { StatusText } from "~/components/status";
import { ButtonLink, TextLink } from "~/components/ui/link";
import { Table, Td, Th } from "~/components/ui/table";
import { api } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { formatDateTime } from "~/lib/format";
import { title } from "~/lib/meta";
import { canWrite } from "~/lib/session";
import type { Ledger, List, Role, Transaction } from "~/lib/types";
import type { Route } from "./+types/overview";

export const meta: Route.MetaFunction = () => title("Overview");

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const [ledgers, transactions] = await Promise.all([
    api<List<Ledger>>("/v1/ledgers?limit=5", { signal: request.signal }),
    api<List<Transaction>>("/v1/transactions?limit=8", { signal: request.signal }),
  ]);
  const lifecycle = transactions.data[0] ? await loadLifecycle(transactions.data[0], request.signal) : null;
  return { ledgers, transactions, lifecycle };
}

const abilities: Record<Role, string> = {
  admin: "Read and change everything, including API keys.",
  write: "Read and change everything except API keys.",
  read: "Read everything except API keys. Nothing can be changed.",
};

export default function Overview({ loaderData }: Route.ComponentProps) {
  const key = useApiKey();
  const reducedMotion = useReducedMotion();
  const { ledgers, transactions, lifecycle } = loaderData;
  const writable = canWrite(key.role);

  return (
    <motion.div
      initial={reducedMotion ? false : { opacity: 0, y: 4 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: reducedMotion ? 0 : 0.18 }}
      className="grid gap-8"
    >
      <PageHeader
        title="Overview"
        description="Your ledgers and latest activity, in one place."
        actions={writable && <ButtonLink to="/transactions/new" variant="primary">New transaction</ButtonLink>}
      />

      <section aria-labelledby="lifecycle-preview">
        <div className="mb-4 flex items-center justify-between gap-4">
          <h2 id="lifecycle-preview" className="font-medium">Lifecycle</h2>
          <TextLink to={withQuery("/lifecycle", { transaction_id: lifecycle?.focus })} className="text-xs text-muted">Explore flows</TextLink>
        </div>
        {lifecycle ? <LifecycleFlow data={lifecycle} compact /> : <p className="border border-dashed border-line-strong p-6 text-muted">No transactions yet.</p>}
      </section>

      <section aria-labelledby="recent-transactions">
        <div className="mb-4 flex items-center justify-between gap-4">
          <h2 id="recent-transactions" className="font-medium">Recent transactions</h2>
          <TextLink to="/transactions" className="text-xs text-muted">View all transactions</TextLink>
        </div>
        {transactions.data.length === 0 ? (
          <div className="grid justify-items-start gap-3 border border-dashed border-line-strong p-6 sm:p-10">
            <p className="font-medium">No transactions yet</p>
            <p className="max-w-md leading-relaxed text-muted">Transactions will appear here as money moves between your accounts.</p>
            {writable && <ButtonLink to={ledgers.data.length ? "/transactions/new" : "/ledgers/new"}>{ledgers.data.length ? "Create a transaction" : "Create your first ledger"}</ButtonLink>}
          </div>
        ) : (
          <Table>
            <thead><tr><Th>Transaction</Th><Th>Status</Th><Th>Effective date</Th><Th numeric>Entries</Th></tr></thead>
            <tbody>
              {transactions.data.map((transaction) => (
                <tr key={transaction.id}>
                  <Td>
                    <TextLink to={`/transactions/${transaction.id}`} className="font-medium">{transaction.description || "No description"}</TextLink>
                    <p className="mt-1 font-mono text-[11px] text-muted">{transaction.external_id || transaction.id}</p>
                  </Td>
                  <Td><StatusText status={transaction.status} /></Td>
                  <Td className="whitespace-nowrap text-muted">{formatDateTime(transaction.effective_at)}</Td>
                  <Td numeric>{transaction.entries.length}</Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </section>

      <div className="grid gap-8 border-t border-line pt-8 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] xl:gap-12">
        <section aria-labelledby="recent-ledgers" className="min-w-0">
          <div className="mb-4 flex items-center justify-between gap-4">
            <h2 id="recent-ledgers" className="font-medium">Recent ledgers</h2>
            <TextLink to="/ledgers" className="text-xs text-muted">View all ledgers</TextLink>
          </div>
          {ledgers.data.length === 0 ? (
            <p className="py-4 leading-relaxed text-muted">No ledgers yet. A ledger groups the accounts that move money between each other.</p>
          ) : (
            <ul className="divide-y divide-line border-y border-line">
              {ledgers.data.map((ledger) => (
                <li key={ledger.id}>
                  <Link to={`/ledgers/${ledger.id}`} className="group flex items-center justify-between gap-4 px-2 py-4 transition-colors hover:bg-sunken motion-reduce:transition-none">
                    <div className="min-w-0">
                      <p className="truncate font-medium">{ledger.name}</p>
                      <p className="mt-1 truncate text-xs text-muted">{ledger.description || `Created ${formatDateTime(ledger.created_at)}`}</p>
                    </div>
                    <span aria-hidden="true" className="text-muted group-hover:text-ink">↗</span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </section>
        <div className="min-w-0">
          <Section title="Your access">
            <p className="leading-relaxed text-muted">{key.role}. {abilities[key.role]}</p>
            <Details
              items={[
                { term: "Name", value: key.name },
                { term: "Key", value: key.hint, mono: true },
                { term: "Created", value: formatDateTime(key.created_at) },
                { term: "Expires", value: key.expires_at ? formatDateTime(key.expires_at) : "Never" },
              ]}
            />
          </Section>
        </div>
      </div>
    </motion.div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
