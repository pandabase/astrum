import { formatAmount, parseAmount } from "./format";
import { parseMetadata } from "./metadata";
import { toApiTime } from "./time";
import type { Account, BalanceCondition, EntryLine, Metadata, Side } from "./types";

export type LockView = "" | "available" | "pending" | "posted";
export type LockOperator = keyof BalanceCondition;

/** One row of the entries editor, as typed: amounts are in the currency's units, such as "12.50". */
export type EntryDraft = {
  key: string;
  accountId: string;
  side: Side;
  amount: string;
  lockView: LockView;
  lockOperator: LockOperator;
  lockAmount: string;
  requireVersion: boolean;
};

export function blankDraft(side: Side = "debit"): EntryDraft {
  return {
    key: crypto.randomUUID(),
    accountId: "",
    side,
    amount: "",
    lockView: "",
    lockOperator: "gte",
    lockAmount: "",
    requireVersion: false,
  };
}

/** Turns API entries back into editable rows, for editing a pending transaction. */
export function draftsFromEntries(entries: EntryLine[], accounts: Map<string, Account>): EntryDraft[] {
  return entries.map((entry) => {
    const exponent = accounts.get(entry.account_id)?.currency_exponent ?? 0;
    return { ...blankDraft(entry.side), accountId: entry.account_id, amount: formatAmount(entry.amount, exponent).replaceAll(",", "") };
  });
}

type Result<T> = { ok: true; value: T } | { ok: false; error: string };

/** Converts rows into API entries, reading each account's exponent from accounts rather than from the form. */
export function entriesFromDrafts(drafts: EntryDraft[], accounts: Map<string, Account>): Result<EntryLine[]> {
  if (drafts.length < 2) return { ok: false, error: "A transaction needs at least two entries." };
  const entries: EntryLine[] = [];
  for (const [i, draft] of drafts.entries()) {
    const row = `Entry ${i + 1}`;
    const account = accounts.get(draft.accountId);
    if (!account) return { ok: false, error: `${row}: choose an account.` };
    const amount = parseAmount(draft.amount, account.currency_exponent);
    if (amount === null || amount.startsWith("-") || amount === "0") {
      return { ok: false, error: `${row}: enter a positive amount with at most ${account.currency_exponent} decimal places.` };
    }
    const entry: EntryLine = { account_id: account.id, side: draft.side, amount };
    if (draft.lockView) {
      const bound = parseAmount(draft.lockAmount, account.currency_exponent);
      if (bound === null) return { ok: false, error: `${row}: the balance condition needs an amount.` };
      entry[`${draft.lockView}_balance_amount`] = { [draft.lockOperator]: bound };
    }
    if (draft.requireVersion) entry.lock_version = account.lock_version;
    entries.push(entry);
  }
  return { ok: true, value: entries };
}

export type CurrencyTotal = { currency: string; exponent: number; debits: bigint; credits: bigint };

/** Debit and credit totals per currency over the rows that have an account and a valid amount. */
export function totals(drafts: EntryDraft[], accounts: Map<string, Account>): CurrencyTotal[] {
  const byCurrency = new Map<string, CurrencyTotal>();
  for (const draft of drafts) {
    const account = accounts.get(draft.accountId);
    const amount = account && parseAmount(draft.amount, account.currency_exponent);
    if (!account || !amount || amount.startsWith("-")) continue;
    const total = byCurrency.get(account.currency) ?? {
      currency: account.currency,
      exponent: account.currency_exponent,
      debits: 0n,
      credits: 0n,
    };
    if (draft.side === "debit") total.debits += BigInt(amount);
    else total.credits += BigInt(amount);
    byCurrency.set(account.currency, total);
  }
  return [...byCurrency.values()].sort((a, b) => a.currency.localeCompare(b.currency));
}

export type TransactionFields = {
  description: string;
  metadata: Metadata;
  effectiveAt: string | null;
  externalId: string;
  status: "posted" | "pending";
  archiveOnLockFailure: boolean;
};

/** Reads the fields around the entries: description, metadata, status and dates. */
export function readTransactionFields(form: FormData): Result<TransactionFields> {
  const value = (name: string) => String(form.get(name) ?? "").trim();
  const metadata = parseMetadata(value("metadata"));
  if (!metadata.ok) return metadata;
  const effectiveText = value("effective_at");
  const effectiveAt = toApiTime(effectiveText);
  if (effectiveText !== "" && effectiveAt === null) return { ok: false, error: "Effective date is not a valid date." };
  return {
    ok: true,
    value: {
      description: value("description"),
      metadata: metadata.value,
      effectiveAt,
      externalId: value("external_id"),
      status: value("status") === "pending" ? "pending" : "posted",
      archiveOnLockFailure: form.get("archive_on_balance_lock_failure") === "on",
    },
  };
}

/** Reads the rows the entries editor serialized into its hidden field. */
export function readDrafts(form: FormData): EntryDraft[] {
  try {
    const drafts = JSON.parse(String(form.get("entries") ?? "[]"));
    return Array.isArray(drafts) ? drafts : [];
  } catch {
    return [];
  }
}

/** The API body shared by creating, batching and scheduling a transaction. */
export function transactionBody(fields: TransactionFields, entries: EntryLine[]) {
  return {
    description: fields.description,
    metadata: fields.metadata,
    entries,
    status: fields.status,
    ...(fields.effectiveAt ? { effective_at: fields.effectiveAt } : {}),
    ...(fields.externalId ? { external_id: fields.externalId } : {}),
    ...(fields.archiveOnLockFailure ? { archive_on_balance_lock_failure: true } : {}),
  };
}
