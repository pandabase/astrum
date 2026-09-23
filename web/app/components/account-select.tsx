import type { ComponentProps } from "react";
import type { Account } from "~/lib/types";
import { Select } from "./ui/field";

/** Chooses one account; closed and frozen accounts are shown but cannot be picked unless allowInactive. */
export function AccountSelect({ accounts, allowInactive, ...props }: ComponentProps<typeof Select> & { accounts: Account[]; allowInactive?: boolean }) {
  return (
    <Select {...props}>
      <option value="">Choose an account</option>
      {accounts.map((a) => (
        <option key={a.id} value={a.id} disabled={!allowInactive && a.status !== "open"}>
          {a.code}
          {a.name ? ` — ${a.name}` : ""} ({a.currency}){a.status !== "open" ? ` [${a.status}]` : ""}
        </option>
      ))}
    </Select>
  );
}
