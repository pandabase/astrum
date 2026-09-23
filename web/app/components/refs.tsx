import type { Account } from "~/lib/types";
import { TextLink } from "./ui/link";

/** Links to an account by its code when it is known, or by id otherwise. */
export function AccountRef({ id, accounts }: { id: string; accounts?: Map<string, Account> }) {
  const account = accounts?.get(id);
  return (
    <TextLink to={`/accounts/${id}`} className="font-mono text-xs">
      {account?.code ?? id}
    </TextLink>
  );
}

const routes: Record<string, string> = {
  txn: "/transactions",
  hold: "/holds",
  sched: "/scheduled",
  stmt: "/statements",
  cat: "/categories",
  stl: "/settlements",
  bm: "/monitors",
  blk: "/bulk",
  evt: "/events",
  we: "/webhooks",
  ldg: "/ledgers",
  acct: "/accounts",
};

/** Links any resource id to its page, chosen by the id's prefix. */
export function IdLink({ id }: { id: string }) {
  const base = routes[id.slice(0, id.indexOf("_"))];
  if (!base) return <span className="font-mono text-xs">{id}</span>;
  return (
    <TextLink to={`${base}/${id}`} className="font-mono text-xs">
      {id}
    </TextLink>
  );
}
