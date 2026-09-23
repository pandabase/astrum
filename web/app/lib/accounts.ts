import { api, fetchAll, path } from "./api";
import { withQuery } from "./query";
import type { Account } from "./types";

// Code, currency and exponent never change, so an account looked up once can label it for the rest of the session.
const known = new Map<string, Account>();

/** Loads the accounts behind ids so pages can show codes and format amounts; unknown ids are left out. */
export async function accountsById(ids: Iterable<string | null | undefined>, signal?: AbortSignal): Promise<Map<string, Account>> {
  const wanted = [...new Set([...ids].filter((id): id is string => Boolean(id)))];
  await Promise.all(
    wanted
      .filter((id) => !known.has(id))
      .map(async (id) => {
        const account = await api<Account>(path`/v1/accounts/${id}`, { signal }).catch(() => null);
        if (account) known.set(id, account);
      }),
  );
  return new Map(wanted.flatMap((id) => (known.has(id) ? [[id, known.get(id)!] as const] : [])));
}

/** Every account in a ledger, for pickers, with fresh balances and lock versions. */
export async function ledgerAccounts(ledgerId: string, signal?: AbortSignal): Promise<Account[]> {
  const accounts = await fetchAll<Account>(withQuery("/v1/accounts", { ledger_id: ledgerId }), { signal });
  for (const account of accounts) known.set(account.id, account);
  return accounts.sort((a, b) => a.code.localeCompare(b.code));
}
