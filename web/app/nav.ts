import type { Role } from "./lib/types";

export type NavItem = {
  to: string;
  label: string;
  /** Hides the item from keys without this role; omitted means every key sees it. */
  role?: Extract<Role, "admin">;
  /** Other path prefixes that belong to this item, such as account pages under Ledgers. */
  alsoActive?: string[];
};

export type NavGroup = { label?: string; items: NavItem[] };

export const navigation: NavGroup[] = [
  { items: [{ to: "/", label: "Overview" }] },
  {
    label: "Ledger",
    items: [
      { to: "/ledgers", label: "Ledgers", alsoActive: ["/accounts"] },
      { to: "/transactions", label: "Transactions", alsoActive: ["/bulk"] },
      { to: "/entries", label: "Entries" },
      { to: "/holds", label: "Holds" },
      { to: "/scheduled", label: "Scheduled" },
      { to: "/currencies", label: "Currencies" },
    ],
  },
  {
    label: "Reporting",
    items: [
      { to: "/categories", label: "Categories" },
      { to: "/statements", label: "Statements" },
      { to: "/settlements", label: "Settlements" },
    ],
  },
  {
    label: "Operations",
    items: [
      { to: "/events", label: "Events" },
      { to: "/webhooks", label: "Webhooks" },
      { to: "/monitors", label: "Balance monitors" },
      { to: "/integrity", label: "Integrity" },
    ],
  },
  { label: "Admin", items: [{ to: "/api-keys", label: "API keys", role: "admin" }] },
];
