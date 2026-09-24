import { afterEach, describe, expect, it, vi } from "vitest";
import { api, fetchAll } from "~/lib/api";
import { accountsById } from "~/lib/accounts";
import { buildLifecycle, layoutLifecycle, loadLifecycle, type LifecycleData } from "~/lib/lifecycle";
import type { Account, Entry, Hold, ScheduledTransaction, Settlement, Transaction } from "~/lib/types";

vi.mock("~/lib/api", async (importOriginal) => ({ ...await importOriginal<typeof import("~/lib/api")>(), api: vi.fn(), fetchAll: vi.fn() }));
vi.mock("~/lib/accounts", () => ({ accountsById: vi.fn() }));

afterEach(() => vi.resetAllMocks());

const time = "2026-09-01T00:00:00Z";
function transaction(id: string, extra: Partial<Transaction> = {}): Transaction {
  return { object: "transaction", id, ledger_id: "ldg_a", idempotency_key: id, external_id: null, status: "posted", version: 1,
    description: id, metadata: {}, reverses_id: null, effective_at: time, created_at: time, posted_at: time, archived_at: null,
    entries: [{ account_id: "acct_a", side: "debit", amount: "100", currency: "USD" }, { account_id: "acct_b", side: "credit", amount: "100", currency: "USD" }], ...extra };
}
function data(extra: Partial<LifecycleData> = {}): LifecycleData {
  return { focus: "txn_a", transactions: [transaction("txn_a")], holds: [], schedules: [], settlements: [], entries: [], accounts: new Map(), limited: false, ...extra };
}
function entry(extra: Partial<Entry> = {}): Entry {
  return { object: "entry", sequence: "1", transaction_id: "txn_a", ledger_id: "ldg_a", account_id: "acct_a", status: "posted", side: "debit", amount: "100", currency: "USD", balance_after: "100", effective_at: time, created_at: time, settlement_id: "stl_a", ...extra };
}
function settlement(): Settlement {
  return { object: "settlement", id: "stl_a", idempotency_key: "settle", ledger_id: "ldg_a", settled_account_id: "acct_a", contra_account_id: "acct_b", currency: "USD", effective_at_upper_bound: null, amount: "100", entry_count: 1, transaction_id: "txn_payout", description: "Payout", metadata: {}, created_at: time };
}

describe("lifecycle relationships", () => {
  it("connects captures, schedules and reversals only through their IDs", () => {
    const hold = { id: "hold_a", account_id: "acct_a", capture_transaction_id: "txn_a", amount: "120", captured_amount: "100", currency: "USD", status: "captured", created_at: time, resolved_at: time } as Hold;
    const schedule = { id: "sched_a", transaction_id: "txn_other", status: "executed", execute_at: time } as ScheduledTransaction;
    const graph = buildLifecycle(data({ transactions: [transaction("txn_a"), transaction("txn_reverse", { reverses_id: "txn_a" }), transaction("txn_other")], holds: [hold], schedules: [schedule] }));
    expect(graph.edges).toContainEqual({ from: "hold_a", to: "txn_a", label: "Captured into" });
    expect(graph.edges).toContainEqual({ from: "txn_a", to: "txn_reverse", label: "Reversed by" });
    expect(graph.edges).toContainEqual({ from: "sched_a", to: "txn_other", label: "Executed as" });
    expect(graph.edges.some((edge) => edge.from === "txn_a" && edge.to === "txn_other")).toBe(false);
    expect(graph.nodes.filter((node) => node.kind === "Debit entries")).toHaveLength(3);
  });

  it("links covered entries to settlement and payout without a self-cycle", () => {
    const graph = buildLifecycle(data({ transactions: [transaction("txn_a"), transaction("txn_payout")], settlements: [settlement()], entries: [entry(), entry({ transaction_id: "txn_payout", sequence: "2" })] }));
    expect(graph.edges).toContainEqual({ from: "txn_a:acct_a:debit", to: "stl_a", label: "Included in" });
    expect(graph.edges).toContainEqual({ from: "stl_a", to: "txn_payout", label: "Created" });
    expect(graph.edges.some((edge) => edge.from.startsWith("txn_payout:") && edge.to === "stl_a")).toBe(false);
    const layout = layoutLifecycle(graph);
    const positions = new Map(layout.nodes.map((node) => [node.id, node]));
    for (const edge of graph.edges) expect(positions.get(edge.to)!.x).toBeGreaterThan(positions.get(edge.from)!.x);
  });

  it("keeps currencies separate and preserves 38-digit precision", () => {
    const large = "99999999999999999999999999999999999990";
    const graph = buildLifecycle(data({ transactions: [transaction("txn_a", { entries: [
      { account_id: "acct_a", side: "debit", amount: large, currency: "USD" },
      { account_id: "acct_a", side: "debit", amount: "1", currency: "USD" },
      { account_id: "acct_eth", side: "debit", amount: "15", currency: "ETH" },
    ] })] }));
    expect(graph.nodes[0].amounts.map((a) => [a.currency, a.value])).toEqual([["USD", "99999999999999999999999999999999999991"], ["ETH", "15"]]);
    expect(graph.nodes.find((node) => node.id === "txn_a:acct_a:debit")!.amounts[0].value).toBe("99999999999999999999999999999999999991");
  });

  it("does not invent a pending stage for a posted transaction", () => {
    const graph = buildLifecycle(data());
    expect(graph.nodes.some((node) => node.status === "pending")).toBe(false);
    expect(graph.nodes.filter((node) => node.kind === "Transaction")).toHaveLength(1);
  });

  it("discloses hidden account groups and retains their settlement connection", () => {
    const entries = Array.from({ length: 15 }, (_, i) => ({ account_id: `acct_${i}`, side: "debit" as const, amount: "1", currency: "USD" }));
    const graph = buildLifecycle(data({ transactions: [transaction("txn_a", { entries })], settlements: [settlement()], entries: [entry({ account_id: "acct_14" })] }));
    expect(graph.hiddenEntries).toBe(3);
    expect(graph.edges).toContainEqual({ from: "txn_a", to: "stl_a", label: "Included in" });
  });
});

describe("lifecycle loading", () => {
  const page = (items: unknown[], more = false) => ({ object: "list", data: items, has_more: more, next_cursor: more ? "next" : null });

  it("fetches old referenced transactions and settlements with cancellation", async () => {
    const signal = new AbortController().signal;
    const root = transaction("txn_a", { reverses_id: "txn_old" });
    vi.mocked(api).mockImplementation(async (url) => {
      if (url === "/v1/transactions/txn_old") return transaction("txn_old");
      if (url === "/v1/transactions/txn_payout") return transaction("txn_payout");
      if (url === "/v1/settlements/stl_a") return settlement();
      if (url.startsWith("/v1/transactions?")) return page([root], true);
      if (url.startsWith("/v1/holds?")) return page([{ id: "hold_unrelated", capture_transaction_id: "txn_unrelated" }]);
      return page([]);
    });
    vi.mocked(fetchAll).mockResolvedValue([entry()]);
    vi.mocked(accountsById).mockResolvedValue(new Map<string, Account>());
    const result = await loadLifecycle(root, signal);
    expect(result.transactions.map((t) => t.id)).toEqual(["txn_a", "txn_old", "txn_payout"]);
    expect(result.holds).toEqual([]);
    expect(result.settlements).toHaveLength(1);
    expect(result.limited).toBe(true);
    for (const [, options] of vi.mocked(api).mock.calls) expect(options?.signal).toBe(signal);
  });

  it("does not fetch posted entries for pending transactions", async () => {
    vi.mocked(api).mockResolvedValue(page([]));
    vi.mocked(accountsById).mockResolvedValue(new Map());
    const result = await loadLifecycle(transaction("txn_a", { status: "pending", posted_at: null }), new AbortController().signal);
    expect(fetchAll).not.toHaveBeenCalled();
    expect(result.entries).toEqual([]);
  });

  it("propagates failures instead of claiming the relationship list is empty", async () => {
    vi.mocked(api).mockRejectedValue(new Error("Unavailable"));
    await expect(loadLifecycle(transaction("txn_a"), new AbortController().signal)).rejects.toThrow("Unavailable");
  });
});
