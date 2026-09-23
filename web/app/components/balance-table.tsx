import type { Balances } from "~/lib/types";
import { Amount } from "./amount";
import { Table, Td, Th } from "./ui/table";

const views = [
  { key: "posted", label: "Posted", note: "Recorded in the journal" },
  { key: "pending", label: "Pending", note: "Posted plus pending transactions" },
  { key: "available", label: "Available", note: "Posted less pending outflows and holds" },
] as const;

/** The three balance views with their debit and credit totals. */
export function BalanceTable({ balances, exponent, currency }: { balances: Balances; exponent: number; currency: string }) {
  return (
    <Table>
      <thead>
        <tr>
          <Th>Balance</Th>
          <Th numeric>Debits</Th>
          <Th numeric>Credits</Th>
          <Th numeric>Amount ({currency})</Th>
        </tr>
      </thead>
      <tbody>
        {views.map(({ key, label, note }) => (
          <tr key={key}>
            <Td>
              {label} <span className="text-muted">— {note}</span>
            </Td>
            <Td numeric>
              <Amount value={balances[key].debits} exponent={exponent} />
            </Td>
            <Td numeric>
              <Amount value={balances[key].credits} exponent={exponent} />
            </Td>
            <Td numeric className="font-medium">
              <Amount value={balances[key].amount} exponent={exponent} />
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
