import type { Account, Entry } from "~/lib/types";
import { Amount } from "./amount";
import { AccountRef, IdLink } from "./refs";
import { StatusText } from "./status";
import { Table, Td, Th } from "./ui/table";
import { formatDateTime } from "~/lib/format";

/** Postings from the entries search, with the account and transaction each belongs to. */
export function EntriesTable({ entries, accounts }: { entries: Entry[]; accounts: Map<string, Account> }) {
  return (
    <Table>
      <thead>
        <tr>
          <Th>Effective</Th>
          <Th>Account</Th>
          <Th>Transaction</Th>
          <Th>Status</Th>
          <Th numeric>Debit</Th>
          <Th numeric>Credit</Th>
          <Th numeric>Balance after</Th>
          <Th>Settled</Th>
        </tr>
      </thead>
      <tbody>
        {entries.map((e) => {
          const exponent = accounts.get(e.account_id)?.currency_exponent ?? 0;
          return (
            <tr key={e.sequence}>
              <Td className="whitespace-nowrap">{formatDateTime(e.effective_at)}</Td>
              <Td>
                <AccountRef id={e.account_id} accounts={accounts} />
              </Td>
              <Td>
                <IdLink id={e.transaction_id} />
              </Td>
              <Td>
                <StatusText status={e.status} />
              </Td>
              <Td numeric>{e.side === "debit" && <Amount value={e.amount} exponent={exponent} />}</Td>
              <Td numeric>{e.side === "credit" && <Amount value={e.amount} exponent={exponent} />}</Td>
              <Td numeric>{e.balance_after !== null ? <Amount value={e.balance_after} exponent={exponent} /> : <span className="text-muted">—</span>}</Td>
              <Td>{e.settlement_id ? <IdLink id={e.settlement_id} /> : <span className="text-muted">—</span>}</Td>
            </tr>
          );
        })}
      </tbody>
    </Table>
  );
}
