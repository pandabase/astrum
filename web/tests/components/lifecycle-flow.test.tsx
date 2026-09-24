import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it } from "vitest";
import { LifecycleFlow } from "~/components/lifecycle-flow";
import type { LifecycleData } from "~/lib/lifecycle";

const time = "2026-09-01T00:00:00Z";
const data: LifecycleData = {
  focus: "txn_a", limited: true, accounts: new Map(), holds: [], schedules: [], settlements: [], entries: [],
  transactions: [{ object: "transaction", id: "txn_a", ledger_id: "ldg_a", idempotency_key: "operation", external_id: null,
    status: "posted", version: 1, description: "Customer payment", metadata: {}, reverses_id: null, effective_at: time,
    created_at: time, posted_at: time, archived_at: null, entries: [
      { account_id: "acct_a", side: "debit", amount: "100", currency: "USD" },
      { account_id: "acct_b", side: "credit", amount: "100", currency: "USD" },
    ] }],
};

afterEach(cleanup);

describe("lifecycle flowchart", () => {
  it("provides keyboard-accessible records, zoom controls and a text alternative", () => {
    render(<MemoryRouter><LifecycleFlow data={data} /></MemoryRouter>);
    expect(screen.getByRole("region", { name: "Lifecycle flowchart" }).tabIndex).toBe(0);
    const records = screen.getByRole("list", { name: "Lifecycle records" });
    const links = within(records).getAllByRole("link");
    expect(links.map((link) => link.getAttribute("href"))).toEqual(["/transactions/txn_a", "/accounts/acct_a", "/accounts/acct_b"]);
    fireEvent.click(screen.getByRole("button", { name: "Zoom in" }));
    expect(screen.getByText("100%")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Zoom out" }));
    expect(screen.getByText("80%")).toBeTruthy();
    fireEvent.click(screen.getByText("Connections"));
    expect(screen.getAllByText(/contains/)).toHaveLength(2);
  });

  it("discloses incomplete discovery and labels unknown currency precision", () => {
    render(<MemoryRouter><LifecycleFlow data={data} /></MemoryRouter>);
    expect(screen.getByText(/Partial view/)).toBeTruthy();
    expect(screen.getAllByText("100 USD minor units")).toHaveLength(3);
  });
});
