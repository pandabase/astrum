import { accountsById } from "./accounts";
import { api, fetchAll, path } from "./api";
import { withQuery } from "./query";
import type { Account, Entry, Hold, List, ScheduledTransaction, Settlement, Transaction } from "./types";

export type LifecycleData = {
  focus: string;
  transactions: Transaction[];
  holds: Hold[];
  schedules: ScheduledTransaction[];
  settlements: Settlement[];
  entries: Entry[];
  accounts: Map<string, Account>;
  limited: boolean;
};

export async function loadLifecycle(transaction: Transaction, signal: AbortSignal): Promise<LifecycleData> {
  const [recent, holds, schedules] = await Promise.all([
    api<List<Transaction>>(withQuery("/v1/transactions", { ledger_id: transaction.ledger_id, limit: 100 }), { signal }),
    api<List<Hold>>("/v1/holds?limit=100", { signal }),
    api<List<ScheduledTransaction>>("/v1/scheduled_transactions?limit=100", { signal }),
  ]);
  const transactions = new Map([[transaction.id, transaction]]);
  for (const candidate of recent.data) {
    if (candidate.reverses_id === transaction.id || candidate.id === transaction.reverses_id) transactions.set(candidate.id, candidate);
  }
  if (transaction.reverses_id && !transactions.has(transaction.reverses_id)) {
    const original = await api<Transaction>(path`/v1/transactions/${transaction.reverses_id}`, { signal });
    transactions.set(original.id, original);
  }
  const entries = (await Promise.all([...transactions.values()].filter((t) => t.status === "posted").map((t) =>
    fetchAll<Entry>(withQuery("/v1/entries", { transaction_id: t.id, status: "posted" }), { signal, max: 1000 }),
  ))).flat();
  const settlementIds = [...new Set(entries.flatMap((entry) => entry.settlement_id ? [entry.settlement_id] : []))];
  const settlements = await Promise.all(settlementIds.slice(0, 20).map((id) => api<Settlement>(path`/v1/settlements/${id}`, { signal })));
  const payoutIds = [...new Set(settlements.flatMap((s) => s.transaction_id && !transactions.has(s.transaction_id) ? [s.transaction_id] : []))];
  for (const payout of await Promise.all(payoutIds.map((id) => api<Transaction>(path`/v1/transactions/${id}`, { signal })))) {
    transactions.set(payout.id, payout);
  }
  const relatedHolds = holds.data.filter((h) => h.capture_transaction_id && transactions.has(h.capture_transaction_id));
  const relatedSchedules = schedules.data.filter((s) => s.transaction_id && transactions.has(s.transaction_id));
  const accountIds = [...new Set([...transactions.values()].flatMap((t) => t.entries.map((entry) => entry.account_id)))];
  const accounts = await accountsById(accountIds.slice(0, 200), signal);
  if (signal.aborted) throw signal.reason;
  return {
    focus: transaction.id,
    transactions: [...transactions.values()],
    holds: relatedHolds,
    schedules: relatedSchedules,
    settlements,
    entries,
    accounts,
    limited: recent.has_more || holds.has_more || schedules.has_more || settlementIds.length > 20 || accountIds.length > 200,
  };
}

export type FlowAmount = { value: string; currency: string; exponent?: number };
export type FlowNode = {
  id: string;
  kind: string;
  label: string;
  status: string;
  detail: string;
  timestamp: string;
  to: string;
  amounts: FlowAmount[];
};
export type FlowEdge = { from: string; to: string; label: string };
export type FlowGraph = { nodes: FlowNode[]; edges: FlowEdge[]; hiddenEntries: number };

function amounts(transaction: Transaction, accounts: Map<string, Account>): FlowAmount[] {
  const totals = new Map<string, FlowAmount>();
  for (const entry of transaction.entries) {
    if (entry.side !== "debit") continue;
    const account = accounts.get(entry.account_id);
    const currency = account?.currency ?? entry.currency ?? "Unknown currency";
    const previous = totals.get(currency);
    totals.set(currency, { currency, exponent: account?.currency_exponent, value: String(BigInt(previous?.value ?? "0") + BigInt(entry.amount)) });
  }
  return [...totals.values()];
}

export function buildLifecycle(data: LifecycleData): FlowGraph {
  const nodes: FlowNode[] = [];
  const edges: FlowEdge[] = [];
  let hiddenEntries = 0;
  const entryNodes = new Map<string, string[]>();
  for (const transaction of data.transactions) {
    nodes.push({
      id: transaction.id, kind: transaction.reverses_id ? "Reversal" : "Transaction",
      label: transaction.description || "Untitled transaction", status: transaction.status,
      detail: transaction.external_id || transaction.id,
      timestamp: transaction.posted_at || transaction.archived_at || transaction.created_at,
      to: `/transactions/${transaction.id}`, amounts: amounts(transaction, data.accounts),
    });
    if (transaction.reverses_id) edges.push({ from: transaction.reverses_id, to: transaction.id, label: "Reversed by" });
    const groups = new Map<string, { accountId: string; side: string; value: bigint; currency: string }>();
    for (const entry of transaction.entries) {
      const key = `${entry.account_id}:${entry.side}`;
      const previous = groups.get(key);
      groups.set(key, { accountId: entry.account_id, side: entry.side, value: (previous?.value ?? 0n) + BigInt(entry.amount), currency: entry.currency ?? "" });
    }
    const visible = [...groups.values()].slice(0, 12);
    hiddenEntries += groups.size - visible.length;
    for (const entry of visible) {
      const account = data.accounts.get(entry.accountId);
      const id = `${transaction.id}:${entry.accountId}:${entry.side}`;
      nodes.push({
        id, kind: `${entry.side === "debit" ? "Debit" : "Credit"} entries`,
        label: account?.name || account?.code || entry.accountId, status: transaction.status,
        detail: account?.code || entry.accountId, timestamp: transaction.effective_at,
        to: `/accounts/${entry.accountId}`,
        amounts: [{ value: String(entry.value), currency: account?.currency ?? entry.currency, exponent: account?.currency_exponent }],
      });
      const groupKey = `${transaction.id}:${entry.accountId}`;
      entryNodes.set(groupKey, [...(entryNodes.get(groupKey) ?? []), id]);
      edges.push({ from: transaction.id, to: id, label: "Contains" });
    }
  }
  for (const hold of data.holds) {
    const account = data.accounts.get(hold.account_id);
    nodes.push({ id: hold.id, kind: "Hold", label: hold.description || "Reserved funds", status: hold.status,
      detail: account?.code || hold.account_id, timestamp: hold.resolved_at || hold.created_at, to: `/holds/${hold.id}`,
      amounts: [{ value: hold.captured_amount ?? hold.amount, currency: hold.currency, exponent: account?.currency_exponent }],
    });
    if (hold.capture_transaction_id) edges.push({ from: hold.id, to: hold.capture_transaction_id, label: "Captured into" });
  }
  for (const schedule of data.schedules) {
    nodes.push({ id: schedule.id, kind: "Schedule", label: schedule.description || "Scheduled transaction", status: schedule.status,
      detail: schedule.id, timestamp: schedule.execute_at, to: `/scheduled/${schedule.id}`, amounts: [],
    });
    if (schedule.transaction_id) edges.push({ from: schedule.id, to: schedule.transaction_id, label: "Executed as" });
  }
  for (const settlement of data.settlements) {
    const account = data.accounts.get(settlement.settled_account_id);
    nodes.push({ id: settlement.id, kind: "Settlement", label: settlement.description || "Account settlement", status: "recorded",
      detail: `${settlement.entry_count} covered entries`, timestamp: settlement.created_at, to: `/settlements/${settlement.id}`,
      amounts: [{ value: settlement.amount, currency: settlement.currency, exponent: account?.currency_exponent }],
    });
    if (settlement.transaction_id) edges.push({ from: settlement.id, to: settlement.transaction_id, label: "Created" });
    for (const entry of data.entries) {
      // A settlement includes its own balancing posting; it is not an input to itself.
      if (entry.settlement_id !== settlement.id || entry.transaction_id === settlement.transaction_id) continue;
      for (const source of entryNodes.get(`${entry.transaction_id}:${entry.account_id}`) ?? [entry.transaction_id]) {
        edges.push({ from: source, to: settlement.id, label: "Included in" });
      }
    }
  }
  const ids = new Set(nodes.map((node) => node.id));
  const uniqueEdges = new Map(edges.filter((edge) => ids.has(edge.from) && ids.has(edge.to)).map((edge) => [`${edge.from}:${edge.to}`, edge]));
  return { nodes, edges: [...uniqueEdges.values()], hiddenEntries };
}

export const nodeWidth = 264;
export const nodeHeight = 176;

export function layoutLifecycle(graph: FlowGraph) {
  const ranks = new Map(graph.nodes.map((node) => [node.id, 0]));
  const incoming = new Map(graph.nodes.map((node) => [node.id, graph.edges.filter((edge) => edge.to === node.id).length]));
  const queue = graph.nodes.filter((node) => incoming.get(node.id) === 0).map((node) => node.id);
  for (let i = 0; i < queue.length; i++) {
    for (const edge of graph.edges.filter((edge) => edge.from === queue[i])) {
      ranks.set(edge.to, Math.max(ranks.get(edge.to)!, ranks.get(edge.from)! + 1));
      incoming.set(edge.to, incoming.get(edge.to)! - 1);
      if (incoming.get(edge.to) === 0) queue.push(edge.to);
    }
  }
  const counts = new Map<number, number>();
  for (const rank of ranks.values()) counts.set(rank, (counts.get(rank) ?? 0) + 1);
  const maxRows = Math.max(1, ...counts.values());
  const rows = new Map<number, number>();
  const nodes = graph.nodes.map((node) => {
    const rank = ranks.get(node.id)!;
    const row = rows.get(rank) ?? 0;
    rows.set(rank, row + 1);
    return {
      ...node, rank,
      x: 32 + rank * (nodeWidth + 128),
      y: 72 + ((maxRows - counts.get(rank)!) / 2 + row) * (nodeHeight + 24),
    };
  });
  const columns = [...counts.keys()].sort((a, b) => a - b).map((rank) => {
    const kinds = new Set(nodes.filter((node) => node.rank === rank).map((node) => node.kind));
    const label = [...kinds].every((kind) => kind.endsWith("entries")) ? "Account entries"
      : [...kinds].every((kind) => kind === "Transaction" || kind === "Reversal") ? "Transactions"
      : kinds.size === 1 ? [...kinds][0] : "Related records";
    return { rank, label, x: 32 + rank * (nodeWidth + 128) };
  });
  return {
    nodes, columns,
    width: Math.max(400, ...nodes.map((n) => n.x + nodeWidth + 32)),
    height: 72 + maxRows * (nodeHeight + 24) + 8,
  };
}
