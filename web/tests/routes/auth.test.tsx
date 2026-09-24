import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { createRoutesStub } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { signOut } from "~/lib/auth";
import { readToken, saveToken } from "~/lib/session";
import type { ApiKey } from "~/lib/types";
import * as layout from "~/routes/app-layout";
import * as overview from "~/routes/overview";
import * as signIn from "~/routes/sign-in";
import * as signOutRoute from "~/routes/sign-out";

// Keep route tests independent of the simulated browser's animation lifecycle.
vi.mock("motion/react", async (importOriginal) => ({
  ...await importOriginal<typeof import("motion/react")>(),
  useReducedMotion: () => true,
}));

const good = "sk_01h455vb4pex5vsknk084sn02q_" + "A".repeat(43);

const key: ApiKey = {
  object: "api_key",
  id: "key_01h455vb4pex5vsknk084sn02q",
  name: "ops",
  role: "admin",
  hint: "…AAAA",
  created_by: null,
  created_at: "2026-09-24T00:00:00Z",
  expires_at: null,
  revoked_at: null,
  last_used_at: null,
};

// The API accepts only the good key.
const fetchMock = vi.fn(async (path: RequestInfo | URL, init?: RequestInit) => {
  const auth = new Headers(init?.headers).get("Authorization");
  if (auth !== `Bearer ${good}`) return Response.json({ status: 401, code: "unauthorized" }, { status: 401 });
  if (["/v1/ledgers", "/v1/transactions", "/v1/holds", "/v1/scheduled_transactions", "/v1/entries"].some((prefix) => String(path).startsWith(prefix))) {
    return Response.json({ object: "list", data: [], has_more: false, next_cursor: null });
  }
  return Response.json(key);
});

// Route components are typed against the real route tree; the stub reproduces that tree.
const Stub = createRoutesStub([
  { path: "/sign-in", Component: signIn.default as never, loader: signIn.clientLoader as never, action: signIn.clientAction as never },
  { path: "/sign-out", loader: signOutRoute.clientLoader, action: signOutRoute.clientAction },
  {
    id: "layout",
    Component: layout.default as never,
    middleware: layout.clientMiddleware as never,
    loader: layout.clientLoader as never,
    children: [
      { index: true, Component: overview.default as never, loader: overview.clientLoader as never },
      { path: "ledgers", Component: () => <p>Ledgers page</p> },
    ],
  },
]);

async function submitKey(value: string) {
  fireEvent.change(await screen.findByLabelText("API key"), { target: { value } });
  fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
}

beforeEach(() => {
  sessionStorage.clear();
  signOut();
  fetchMock.mockClear();
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("authentication", () => {
  it("sends a signed-out visitor to sign-in and back to the page they wanted", async () => {
    render(<Stub initialEntries={["/ledgers"]} />);

    await screen.findByRole("heading", { name: "Sign in to Astrum" });
    await submitKey(good);

    await screen.findByText("Ledgers page");
    expect(readToken()).toBe(good);
    expect(screen.getByText("ops")).toBeTruthy();
  });

  it("rejects text that is not a key without calling the API", async () => {
    render(<Stub initialEntries={["/sign-in"]} />);

    await submitKey("password123");

    await screen.findByText("API keys start with sk_.");
    expect(fetchMock).not.toHaveBeenCalled();
    expect(readToken()).toBeNull();
  });

  it("rejects a key the API does not accept", async () => {
    render(<Stub initialEntries={["/sign-in"]} />);

    await submitKey("sk_01h455vb4pex5vsknk084sn02q_" + "B".repeat(43));

    await screen.findByText("This key is invalid, expired or revoked.");
    expect(readToken()).toBeNull();
  });

  it("ignores a next parameter that leaves the site", async () => {
    render(<Stub initialEntries={["/sign-in?next=//evil.example"]} />);

    await submitKey(good);

    await screen.findByText("admin. Read and change everything, including API keys.");
  });

  it("signs out a key the API has revoked", async () => {
    saveToken("sk_revoked");
    render(<Stub initialEntries={["/"]} />);

    await screen.findByRole("heading", { name: "Sign in to Astrum" });
    expect(readToken()).toBeNull();
  });

  it("loads an empty overview and offers the first ledger action", async () => {
    saveToken(good);
    render(<Stub initialEntries={["/"]} />);

    await screen.findByText("No transactions yet");
    expect(screen.getByRole("link", { name: "Create your first ledger" }).getAttribute("href")).toBe("/ledgers/new");
    expect(fetchMock.mock.calls.map(([path]) => path)).toContain("/v1/transactions?limit=8");
  });

  it("shows recent activity without write actions for a read key", async () => {
    fetchMock.mockImplementationOnce(async () => Response.json({ ...key, role: "read" }));
    fetchMock.mockImplementationOnce(async () => Response.json({
      object: "list", data: [{ id: "ldg_example", name: "Customer wallets", description: "USD wallets", created_at: key.created_at }],
      has_more: false, next_cursor: null,
    }));
    fetchMock.mockImplementationOnce(async () => Response.json({
      object: "list", data: [{ id: "txn_example", description: "Opening deposit", status: "posted", external_id: null, ledger_id: "ldg_example", reverses_id: null, entries: [], created_at: key.created_at, posted_at: key.created_at, archived_at: null, effective_at: key.created_at }],
      has_more: false, next_cursor: null,
    }));
    saveToken(good);
    render(<Stub initialEntries={["/"]} />);

    expect((await screen.findByRole("link", { name: "Opening deposit" })).getAttribute("href")).toBe("/transactions/txn_example");
    expect(screen.getByText("Customer wallets")).toBeTruthy();
    expect(screen.queryByRole("link", { name: "New transaction" })).toBeNull();
    expect(screen.queryByRole("link", { name: "API keys" })).toBeNull();
  });

  it("closes mobile navigation after choosing a page", async () => {
    saveToken(good);
    render(<Stub initialEntries={["/"]} />);

    fireEvent.click(await screen.findByRole("button", { name: "Menu" }));
    expect(screen.getByRole("button", { name: "Close menu" }).getAttribute("aria-expanded")).toBe("true");
    fireEvent.click(screen.getByRole("link", { name: "Ledgers" }));

    await screen.findByText("Ledgers page");
    expect(screen.getByRole("button", { name: "Menu" }).getAttribute("aria-expanded")).toBe("false");
  });

  it("signs out on request", async () => {
    saveToken(good);
    render(<Stub initialEntries={["/"]} />);

    fireEvent.click(await screen.findByRole("button", { name: "Sign out" }));

    await screen.findByRole("heading", { name: "Sign in to Astrum" });
    expect(readToken()).toBeNull();
  });
});
