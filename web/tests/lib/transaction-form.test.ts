import { describe, expect, it } from "vitest";
import { blankDraft, draftsFromEntries, entriesFromDrafts, readTransactionFields, totals, type EntryDraft } from "~/lib/transaction-form";
import type { Account } from "~/lib/types";

function account(id: string, currency: string, exponent: number, lockVersion = 3): Account {
  return {
    object: "account",
    id,
    ledger_id: "ldg_1",
    code: id,
    name: "",
    description: "",
    metadata: {},
    currency,
    currency_exponent: exponent,
    normal_side: "debit",
    allow_negative: false,
    overdraft_limit: "0",
    balances: {
      pending: { debits: "0", credits: "0", amount: "0" },
      posted: { debits: "0", credits: "0", amount: "0" },
      available: { debits: "0", credits: "0", amount: "0" },
    },
    held: "0",
    status: "open",
    lock_version: lockVersion,
    status_changed_at: null,
    created_at: "2026-09-24T00:00:00Z",
  };
}

const accounts = new Map([
  ["cash", account("cash", "USD", 2)],
  ["alice", account("alice", "USD", 2, 7)],
  ["eth", account("eth", "ETH", 18)],
]);

const row = (accountId: string, side: "debit" | "credit", amount: string, extra: Partial<EntryDraft> = {}): EntryDraft => ({
  ...blankDraft(side),
  accountId,
  amount,
  ...extra,
});

describe("entriesFromDrafts", () => {
  it("converts amounts with each account's exponent", () => {
    const result = entriesFromDrafts([row("cash", "debit", "12.5"), row("alice", "credit", "12.50"), row("eth", "debit", "0.5")], accounts);
    expect(result).toEqual({
      ok: true,
      value: [
        { account_id: "cash", side: "debit", amount: "1250" },
        { account_id: "alice", side: "credit", amount: "1250" },
        { account_id: "eth", side: "debit", amount: "500000000000000000" },
      ],
    });
  });

  it("adds balance locks and lock versions", () => {
    const result = entriesFromDrafts(
      [
        row("alice", "debit", "10", { lockView: "available", lockOperator: "gte", lockAmount: "5", requireVersion: true }),
        row("cash", "credit", "10"),
      ],
      accounts,
    );
    expect(result.ok && result.value[0]).toEqual({
      account_id: "alice",
      side: "debit",
      amount: "1000",
      available_balance_amount: { gte: "500" },
      lock_version: 7,
    });
  });

  it.each([
    [[row("cash", "debit", "1")], "at least two entries"],
    [[row("", "debit", "1"), row("cash", "credit", "1")], "Entry 1: choose an account"],
    [[row("cash", "debit", "1.234"), row("alice", "credit", "1")], "Entry 1: enter a positive amount with at most 2"],
    [[row("cash", "debit", "0"), row("alice", "credit", "1")], "Entry 1: enter a positive amount"],
    [[row("cash", "debit", "-1"), row("alice", "credit", "1")], "Entry 1: enter a positive amount"],
    [[row("cash", "debit", "1"), row("alice", "credit", "1", { lockView: "posted", lockAmount: "" })], "Entry 2: the balance condition"],
  ])("rejects %j", (drafts, message) => {
    const result = entriesFromDrafts(drafts, accounts);
    expect(result.ok).toBe(false);
    expect(!result.ok && result.error).toContain(message);
  });
});

describe("totals", () => {
  it("sums per currency and skips incomplete rows", () => {
    expect(
      totals([row("cash", "debit", "1.25"), row("alice", "credit", "1"), row("eth", "debit", "0.5"), row("", "credit", "9"), row("cash", "credit", "x")], accounts),
    ).toEqual([
      { currency: "ETH", exponent: 18, debits: 500000000000000000n, credits: 0n },
      { currency: "USD", exponent: 2, debits: 125n, credits: 100n },
    ]);
  });
});

describe("draftsFromEntries", () => {
  it("shows amounts in currency units without separators", () => {
    const drafts = draftsFromEntries([{ account_id: "cash", side: "credit", amount: "123456" }], accounts);
    expect(drafts[0]).toMatchObject({ accountId: "cash", side: "credit", amount: "1234.56" });
  });
});

describe("readTransactionFields", () => {
  it("reads status, dates and flags", () => {
    const form = new FormData();
    form.set("description", " Rent ");
    form.set("status", "pending");
    form.set("effective_at", "2026-09-01T00:00");
    form.set("external_id", "inv-1");
    form.set("archive_on_balance_lock_failure", "on");
    const result = readTransactionFields(form);
    expect(result.ok && result.value).toMatchObject({
      description: "Rent",
      status: "pending",
      externalId: "inv-1",
      archiveOnLockFailure: true,
      metadata: {},
    });
    expect(result.ok && result.value.effectiveAt).toMatch(/^2026-0[89]-/);
  });

  it("rejects bad metadata and dates", () => {
    const form = new FormData();
    form.set("metadata", "[]");
    expect(readTransactionFields(form).ok).toBe(false);
    form.set("metadata", "");
    form.set("effective_at", "yesterday");
    expect(readTransactionFields(form).ok).toBe(false);
  });
});
