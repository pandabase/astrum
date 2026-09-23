import { CopyValue } from "~/components/copy-value";
import { PageHeader } from "~/components/page";
import { Pagination } from "~/components/pagination";
import { ButtonLink, TextLink } from "~/components/ui/link";
import { Notice } from "~/components/ui/notice";
import { Table, Td, Th } from "~/components/ui/table";
import { api } from "~/lib/api";
import { useApiKey } from "~/lib/auth";
import { formatDateTime } from "~/lib/format";
import { title } from "~/lib/meta";
import { searchParams, withQuery } from "~/lib/query";
import { canWrite } from "~/lib/session";
import type { Ledger, List } from "~/lib/types";
import type { Route } from "./+types/index";

export const meta: Route.MetaFunction = () => title("Ledgers");

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  const { cursor } = searchParams(request, []);
  return api<List<Ledger>>(withQuery("/v1/ledgers", { cursor, limit: 50 }), { signal: request.signal });
}

export default function Ledgers({ loaderData: ledgers }: Route.ComponentProps) {
  const key = useApiKey();
  return (
    <div className="grid gap-6">
      <PageHeader
        title="Ledgers"
        actions={canWrite(key.role) && <ButtonLink to="/ledgers/new" variant="primary">New ledger</ButtonLink>}
      />
      {ledgers.data.length === 0 ? (
        <Notice>No ledgers yet. A ledger holds a set of accounts that can move money between each other.</Notice>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Name</Th>
              <Th>Description</Th>
              <Th>ID</Th>
              <Th>Created</Th>
            </tr>
          </thead>
          <tbody>
            {ledgers.data.map((ledger) => (
              <tr key={ledger.id}>
                <Td>
                  <TextLink to={`/ledgers/${ledger.id}`}>{ledger.name}</TextLink>
                </Td>
                <Td className="text-muted">{ledger.description}</Td>
                <Td><CopyValue value={ledger.id} /></Td>
                <Td className="whitespace-nowrap">{formatDateTime(ledger.created_at)}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      <Pagination nextCursor={ledgers.next_cursor} />
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
