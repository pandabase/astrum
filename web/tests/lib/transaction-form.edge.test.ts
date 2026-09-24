import { describe, expect, it } from "vitest";
import {
  blankDraft,
  draftsFromEntries,
  entriesFromDrafts,
  readDrafts,
  readTransactionFields,
  totals,
  transactionBody,
  type EntryDraft,
} from "~/lib/transaction-form";
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
  ["usd1", account("usd1", "USD", 2)],
  ["usd2", account("usd2", "USD", 2, 0)],
  ["jpy", account("jpy", "JPY", 0)],
  ["eth", account("eth", "ETH", 18)],
  ["big", account("big", "BIG", 30)],
]);

const row = (accountId: string, side: "debit" | "credit", amount: string, extra: Partial<EntryDraft> = {}): EntryDraft => ({
  ...blankDraft(side),
  accountId,
  amount,
  ...extra,
});

function fail(drafts: EntryDraft[]) {
  const result = entriesFromDrafts(drafts, accounts);
  return result.ok ? null : result.error;
}

function form(values: Record<string, string>) {
  const data = new FormData();
  for (const [name, value] of Object.entries(values)) data.set(name, value);
  return data;
}

describe("blankDraft", () => {
  it("defaults to a debit with no lock and a unique key", () => {
    const a = blankDraft();
    expect(a).toMatchObject({ accountId: "", side: "debit", amount: "", lockView: "", lockOperator: "gte", lockAmount: "", requireVersion: false });
    expect(blankDraft("credit").side).toBe("credit");
    expect(blankDraft().key).not.toBe(a.key);
  });
});

describe("entriesFromDrafts edge cases", () => {
  it("rejects no rows", () => {
    expect(fail([])).toBe("A transaction needs at least two entries.");
  });

  it("rejects blank rows instead of skipping them", () => {
    expect(fail([row("usd1", "debit", "1"), row("usd2", "credit", "1"), blankDraft()])).toBe("Entry 3: choose an account.");
  });

  it("rejects a row whose account was removed from the list", () => {
    expect(fail([row("usd1", "debit", "1"), row("gone", "credit", "1")])).toBe("Entry 2: choose an account.");
  });

  it.each(["", " ", "0.00", "-0", "+1", ".5", "1e2", "abc"])("rejects the amount %j", (amount) => {
    expect(fail([row("usd1", "debit", amount), row("usd2", "credit", "1")])).toBe("Entry 1: enter a positive amount with at most 2 decimal places.");
  });

  it("names the exponent of the row's own currency", () => {
    expect(fail([row("usd1", "debit", "1"), row("jpy", "credit", "1.5")])).toBe("Entry 2: enter a positive amount with at most 0 decimal places.");
  });

  it("reports the first failing row", () => {
    expect(fail([row("usd1", "debit", "x"), row("", "credit", "1")])).toContain("Entry 1");
  });

  it("converts the smallest and largest amounts per exponent", () => {
    const result = entriesFromDrafts(
      [row("usd1", "debit", "0.01"), row("jpy", "credit", "1,000"), row("eth", "debit", "0.000000000000000001"), row("big", "credit", "99999999")],
      accounts,
    );
    expect(result.ok && result.value.map((e) => e.amount)).toEqual(["1", "1000", "1", "99999999" + "0".repeat(30)]);
  });

  it("does not cap amounts at 38 digits, leaving that to the API", () => {
    const result = entriesFromDrafts([row("usd1", "debit", "9".repeat(39)), row("usd2", "credit", "1")], accounts);
    expect(result.ok && result.value[0].amount).toBe("9".repeat(39) + "00");
  });

  it("allows unbalanced rows, leaving that check to totals and the API", () => {
    expect(entriesFromDrafts([row("usd1", "debit", "1"), row("usd2", "debit", "2")], accounts).ok).toBe(true);
  });

  it("uses the account's id, not the typed one", () => {
    const map = new Map([["alias", account("acct_real", "USD", 2)], ["usd2", accounts.get("usd2")!]]);
    const result = entriesFromDrafts([row("alias", "debit", "1"), row("usd2", "credit", "1")], map);
    expect(result.ok && result.value[0].account_id).toBe("acct_real");
  });

  it.each(["pending", "posted", "available"] as const)("builds a %s balance condition", (view) => {
    const result = entriesFromDrafts([row("usd1", "debit", "1", { lockView: view, lockOperator: "lt", lockAmount: "2.5" }), row("usd2", "credit", "1")], accounts);
    expect(result.ok && result.value[0]).toEqual({ account_id: "usd1", side: "debit", amount: "100", [`${view}_balance_amount`]: { lt: "250" } });
  });

  it.each(["gt", "gte", "eq", "lt", "lte", "not_eq"] as const)("uses the %s operator", (op) => {
    const result = entriesFromDrafts([row("usd1", "debit", "1", { lockView: "posted", lockOperator: op, lockAmount: "0" }), row("usd2", "credit", "1")], accounts);
    expect(result.ok && result.value[0].posted_balance_amount).toEqual({ [op]: "0" });
  });

  it("allows negative and zero lock amounts", () => {
    const result = entriesFromDrafts(
      [row("usd1", "debit", "1", { lockView: "available", lockAmount: "-10.5" }), row("usd2", "credit", "1", { lockView: "posted", lockAmount: "0" })],
      accounts,
    );
    expect(result.ok && result.value.map((e) => e.available_balance_amount ?? e.posted_balance_amount)).toEqual([{ gte: "-1050" }, { gte: "0" }]);
  });

  it("rejects a lock amount with too many decimals for the currency", () => {
    expect(fail([row("usd1", "debit", "1", { lockView: "posted", lockAmount: "1.001" }), row("usd2", "credit", "1")])).toBe(
      "Entry 1: the balance condition needs an amount.",
    );
  });

  it("ignores a lock amount when no lock view is chosen", () => {
    const result = entriesFromDrafts([row("usd1", "debit", "1", { lockAmount: "garbage" }), row("usd2", "credit", "1")], accounts);
    expect(result.ok && result.value[0]).toEqual({ account_id: "usd1", side: "debit", amount: "100" });
  });

  it("sends lock version 0 and omits it when not required", () => {
    const result = entriesFromDrafts([row("usd2", "debit", "1", { requireVersion: true }), row("usd1", "credit", "1")], accounts);
    expect(result.ok && result.value[0].lock_version).toBe(0);
    expect(result.ok && "lock_version" in result.value[1]).toBe(false);
  });
});

describe("totals edge cases", () => {
  it("is empty without rows", () => {
    expect(totals([], accounts)).toEqual([]);
  });

  it("skips negative, invalid and unknown rows but counts zero", () => {
    expect(totals([row("usd1", "debit", "-1"), row("usd1", "debit", "1.001"), row("gone", "debit", "1"), row("usd1", "credit", "0")], accounts)).toEqual([
      { currency: "USD", exponent: 2, debits: 0n, credits: 0n },
    ]);
  });

  it("adds past Number precision with BigInt", () => {
    const max = "9".repeat(38);
    const result = totals([row("jpy", "debit", max), row("jpy", "debit", max), row("jpy", "credit", "1")], accounts);
    expect(result).toEqual([{ currency: "JPY", exponent: 0, debits: BigInt(max) * 2n, credits: 1n }]);
    expect(result[0].debits.toString()).toBe("199999999999999999999999999999999999998");
  });

  it("detects imbalance per currency even when the grand totals match", () => {
    const result = totals([row("usd1", "debit", "1"), row("jpy", "credit", "100")], accounts);
    expect(result.map((t) => [t.currency, t.debits === t.credits])).toEqual([
      ["JPY", false],
      ["USD", false],
    ]);
  });

  it("balances across several accounts of one currency", () => {
    const [usd] = totals([row("usd1", "debit", "10.00"), row("usd2", "credit", "4"), row("usd1", "credit", "6")], accounts);
    expect(usd.debits).toBe(usd.credits);
  });

  it("sorts currencies by code", () => {
    expect(totals([row("usd1", "debit", "1"), row("big", "debit", "1"), row("eth", "debit", "1"), row("jpy", "debit", "1")], accounts).map((t) => t.currency)).toEqual([
      "BIG",
      "ETH",
      "JPY",
      "USD",
    ]);
  });
});

describe("draftsFromEntries edge cases", () => {
  it("is empty without entries", () => {
    expect(draftsFromEntries([], accounts)).toEqual([]);
  });

  it("falls back to exponent 0 for an unknown account", () => {
    expect(draftsFromEntries([{ account_id: "gone", side: "debit", amount: "1234567" }], accounts)[0]).toMatchObject({ accountId: "gone", amount: "1234567" });
  });

  it("round-trips amounts through entriesFromDrafts", () => {
    const entries = [
      { account_id: "eth", side: "debit" as const, amount: "1" },
      { account_id: "jpy", side: "credit" as const, amount: "1234567" },
      { account_id: "big", side: "debit" as const, amount: "9".repeat(38) },
    ];
    const result = entriesFromDrafts(draftsFromEntries(entries, accounts), accounts);
    expect(result.ok && result.value).toEqual(entries);
  });

  it("drops balance conditions and lock versions", () => {
    const [draft] = draftsFromEntries([{ account_id: "usd1", side: "debit", amount: "1", lock_version: 3, posted_balance_amount: { gte: "0" } }], accounts);
    expect(draft).toMatchObject({ lockView: "", lockAmount: "", requireVersion: false });
  });
});

describe("readDrafts", () => {
  it.each([
    [{}, []],
    [{ entries: "" }, []],
    [{ entries: "{" }, []],
    [{ entries: "{}" }, []],
    [{ entries: "null" }, []],
    [{ entries: '"x"' }, []],
    [{ entries: "[]" }, []],
    [{ entries: '[{"accountId":"a"}]' }, [{ accountId: "a" }]],
  ])("reads %j as %j", (values, want) => {
    expect(readDrafts(form(values as Record<string, string>))).toEqual(want);
  });
});

describe("readTransactionFields edge cases", () => {
  it("defaults everything", () => {
    expect(readTransactionFields(new FormData())).toEqual({
      ok: true,
      value: { description: "", metadata: {}, effectiveAt: null, externalId: "", status: "posted", archiveOnLockFailure: false },
    });
  });

  it.each(["", "posted", "archived", "PENDING", " pending "])("reads status %j", (status) => {
    const result = readTransactionFields(form({ status }));
    expect(result.ok && result.value.status).toBe(status.trim() === "pending" ? "pending" : "posted");
  });

  it.each(["true", "1", "off", ""])("only treats on as archiving, not %j", (flag) => {
    const result = readTransactionFields(form({ archive_on_balance_lock_failure: flag }));
    expect(result.ok && result.value.archiveOnLockFailure).toBe(false);
  });

  it("treats a whitespace-only date as none", () => {
    const result = readTransactionFields(form({ effective_at: "   " }));
    expect(result.ok && result.value.effectiveAt).toBeNull();
  });

  it("reports the metadata error before the date error", () => {
    expect(readTransactionFields(form({ metadata: "{", effective_at: "nope" }))).toEqual({ ok: false, error: "Metadata must be valid JSON." });
    expect(readTransactionFields(form({ effective_at: "nope" }))).toEqual({ ok: false, error: "Effective date is not a valid date." });
  });

  it("trims the external id", () => {
    const result = readTransactionFields(form({ external_id: "  inv-1 \n" }));
    expect(result.ok && result.value.externalId).toBe("inv-1");
  });
});

describe("transactionBody", () => {
  const entries = [{ account_id: "a", side: "debit" as const, amount: "1" }];

  it("leaves out optional fields that are empty", () => {
    const body = transactionBody({ description: "", metadata: {}, effectiveAt: null, externalId: "", status: "posted", archiveOnLockFailure: false }, entries);
    expect(body).toEqual({ description: "", metadata: {}, entries, status: "posted" });
  });

  it("includes optional fields that are set", () => {
    const body = transactionBody(
      { description: "d", metadata: { a: 1 }, effectiveAt: "2026-09-24T00:00:00.000Z", externalId: "x", status: "pending", archiveOnLockFailure: true },
      entries,
    );
    expect(body).toEqual({
      description: "d",
      metadata: { a: 1 },
      entries,
      status: "pending",
      effective_at: "2026-09-24T00:00:00.000Z",
      external_id: "x",
      archive_on_balance_lock_failure: true,
    });
  });
});
