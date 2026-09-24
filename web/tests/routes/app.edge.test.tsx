import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createRoutesStub, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { signOut } from "~/lib/auth";
import { readToken, saveToken } from "~/lib/session";
import type { ApiKey, Role } from "~/lib/types";
import * as apiKeys from "~/routes/api-keys";
import * as layout from "~/routes/app-layout";
import * as ledgerDetail from "~/routes/ledgers/detail";
import * as ledgerEdit from "~/routes/ledgers/edit";
import * as ledgers from "~/routes/ledgers/index";
import * as ledgerNew from "~/routes/ledgers/new";
import * as notFound from "~/routes/not-found";
import * as overview from "~/routes/overview";
import * as signIn from "~/routes/sign-in";
import * as signOutRoute from "~/routes/sign-out";

vi.mock("motion/react", async (importOriginal) => ({
  ...await importOriginal<typeof import("motion/react")>(),
  useReducedMotion: () => true,
}));

const good = "sk_01h455vb4pex5vsknk084sn02q_" + "A".repeat(43);
const ledgerId = "ldg_01h455vb4pex5vsknk084sn02q";
const created = "2026-09-24T00:00:00Z";
const empty = { object: "list", data: [], has_more: false, next_cursor: null };

let role: Role = "admin";
let handler: (path: string, init: RequestInit | undefined) => Response | undefined = () => undefined;

function key(): ApiKey {
  return {
    object: "api_key",
    id: "key_01h455vb4pex5vsknk084sn02q",
    name: "ops",
    role,
    hint: "…AAAA",
    created_by: null,
    created_at: created,
    expires_at: null,
    revoked_at: null,
    last_used_at: null,
  };
}

const ledger = { object: "ledger", id: ledgerId, name: "Wallets", description: "", metadata: { team: "core", region: "eu" }, created_at: created };

const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  const path = String(input);
  if (new Headers(init?.headers).get("Authorization") !== `Bearer ${good}`) {
    return Response.json({ status: 401, code: "unauthorized" }, { status: 401 });
  }
  if (path === "/v1/me") return Response.json(key());
  const custom = handler(path, init);
  if (custom) return custom;
  if (path === `/v1/ledgers/${ledgerId}`) return Response.json(ledger);
  return Response.json(empty);
});

function paths() {
  return fetchMock.mock.calls.map(([path]) => String(path));
}

function Probe() {
  const { pathname, search } = useLocation();
  return <p>At {pathname + search}</p>;
}

const appRoutes = [
  { index: true, Component: overview.default as never, loader: overview.clientLoader as never },
  { path: "ledgers", Component: ledgers.default as never, loader: ledgers.clientLoader as never, ErrorBoundary: ledgers.ErrorBoundary },
  { path: "ledgers/new", Component: ledgerNew.default as never, action: ledgerNew.clientAction as never, ErrorBoundary: ledgerNew.ErrorBoundary },
  {
    path: "ledgers/:ledgerId",
    Component: ledgerDetail.default as never,
    loader: ledgerDetail.clientLoader as never,
    ErrorBoundary: ledgerDetail.ErrorBoundary,
  },
  {
    path: "ledgers/:ledgerId/edit",
    Component: ledgerEdit.default as never,
    loader: ledgerEdit.clientLoader as never,
    action: ledgerEdit.clientAction as never,
    ErrorBoundary: ledgerEdit.ErrorBoundary,
  },
  { path: "api-keys", Component: apiKeys.default as never, loader: apiKeys.clientLoader as never, action: apiKeys.clientAction as never, ErrorBoundary: apiKeys.ErrorBoundary },
  { path: "*", Component: notFound.default, loader: notFound.clientLoader, ErrorBoundary: notFound.ErrorBoundary },
];

const layoutRoute = {
  id: "layout",
  Component: layout.default as never,
  middleware: layout.clientMiddleware as never,
  loader: layout.clientLoader as never,
  children: appRoutes,
};

const Stub = createRoutesStub([
  { path: "/sign-in", Component: signIn.default as never, loader: signIn.clientLoader as never, action: signIn.clientAction as never, ErrorBoundary: signIn.ErrorBoundary },
  { path: "/sign-out", loader: signOutRoute.clientLoader, action: signOutRoute.clientAction },
  layoutRoute,
]);

const ProbeStub = createRoutesStub([{ path: "/sign-in", Component: Probe }, layoutRoute]);

async function submitKey(value: string) {
  fireEvent.change(await screen.findByLabelText("API key"), { target: { value } });
  fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
}

beforeEach(() => {
  sessionStorage.clear();
  signOut();
  role = "admin";
  handler = () => undefined;
  fetchMock.mockClear();
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("auth guard", () => {
  it("sends a signed-out visitor to sign-in with the full page as next", async () => {
    render(<ProbeStub initialEntries={[`/ledgers/${ledgerId}?status=frozen&code=a%26b`]} />);
    const next = `/ledgers/${ledgerId}?status=frozen&code=a%26b`;
    await screen.findByText(`At /sign-in?next=${encodeURIComponent(next)}`);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("sends a signed-out visitor from home to plain sign-in", async () => {
    render(<ProbeStub initialEntries={["/"]} />);
    await screen.findByText("At /sign-in");
  });

  it("returns to the filtered page after signing in", async () => {
    render(<Stub initialEntries={[`/ledgers/${ledgerId}?status=frozen`]} />);
    await submitKey(good);
    await screen.findByText("No accounts match these filters.");
    expect(paths()).toContain(`/v1/accounts?ledger_id=${ledgerId}&status=frozen&limit=50`);
  });

  it("skips sign-in for a signed-in visitor and follows next", async () => {
    saveToken(good);
    render(<Stub initialEntries={["/sign-in?next=%2Fledgers"]} />);
    await screen.findByRole("heading", { name: "Ledgers" });
  });

  it("skips sign-in for a signed-in visitor with an unsafe next", async () => {
    saveToken(good);
    render(<Stub initialEntries={["/sign-in?next=https%3A%2F%2Fevil.example"]} />);
    await screen.findByText("admin. Read and change everything, including API keys.");
  });

  it("does not loop when next points at sign-in", async () => {
    render(<Stub initialEntries={["/sign-in?next=%2Fsign-in"]} />);
    await submitKey(good);
    await screen.findByText("admin. Read and change everything, including API keys.");
  });

  it("trims a pasted key", async () => {
    render(<Stub initialEntries={["/sign-in"]} />);
    await submitKey(`  ${good}\n`);
    await screen.findByText("admin. Read and change everything, including API keys.");
    expect(readToken()).toBe(good);
  });

  it("rejects a blank key without calling the API", async () => {
    render(<Stub initialEntries={["/sign-in"]} />);
    const input = await screen.findByLabelText("API key");
    fireEvent.change(input, { target: { value: "   " } });
    fireEvent.submit(input.closest("form")!);
    await screen.findByText("API keys start with sk_.");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("shows the error boundary when the API cannot be reached during sign-in", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => { throw new TypeError("Failed to fetch"); }));
    render(<Stub initialEntries={["/sign-in"]} />);
    await submitKey(good);
    await screen.findByRole("heading", { name: "Cannot reach Astrum" });
    expect(readToken()).toBeNull();
  });

  it("goes home without signing out when sign-out is visited with GET", async () => {
    saveToken(good);
    render(<Stub initialEntries={["/sign-out"]} />);
    await screen.findByText("admin. Read and change everything, including API keys.");
    expect(readToken()).toBe(good);
  });

  it("signs out a key revoked mid-session on the next navigation", async () => {
    saveToken(good);
    render(<Stub initialEntries={["/"]} />);
    await screen.findByText("admin. Read and change everything, including API keys.");
    handler = () => Response.json({ code: "unauthorized" }, { status: 401 });
    fireEvent.click(screen.getByRole("link", { name: "Ledgers" }));
    await screen.findByRole("heading", { name: "Sign in to Astrum" });
    expect(readToken()).toBeNull();
  });
});

describe("role gating", () => {
  it.each([
    ["admin", true, true],
    ["write", true, false],
    ["read", false, false],
  ] as const)("%s key: write actions %s, API keys nav %s", async (r, write, admin) => {
    role = r;
    saveToken(good);
    render(<Stub initialEntries={["/ledgers"]} />);
    await screen.findByRole("heading", { name: "Ledgers" });
    expect(screen.queryByRole("link", { name: "New ledger" }) !== null).toBe(write);
    expect(screen.queryByRole("link", { name: "API keys" }) !== null).toBe(admin);
    expect(screen.getByText(`${r} access`)).toBeTruthy();
  });

  it("hides edit and new account for a read key on a ledger", async () => {
    role = "read";
    saveToken(good);
    render(<Stub initialEntries={[`/ledgers/${ledgerId}`]} />);
    await screen.findByRole("heading", { name: "Wallets" });
    expect(screen.queryByRole("link", { name: "Edit" })).toBeNull();
    expect(screen.queryByRole("link", { name: "New account" })).toBeNull();
    expect(screen.getAllByRole("link", { name: "Transactions" }).map((link) => link.getAttribute("href"))).toContain(`/transactions?ledger_id=${ledgerId}`);
  });

  it("shows Not allowed when a non-admin opens the API keys page directly", async () => {
    role = "write";
    saveToken(good);
    handler = (path) => (path.startsWith("/v1/api_keys") ? Response.json({ code: "forbidden", detail: "role", request_id: "req_403" }, { status: 403 }) : undefined);
    render(<Stub initialEntries={["/api-keys"]} />);
    await screen.findByRole("heading", { name: "Not allowed" });
    expect(screen.getByText("Your API key's role does not allow this.")).toBeTruthy();
    expect(screen.getByText("Request req_403")).toBeTruthy();
    expect(readToken()).toBe(good);
  });

  it("marks the session's own key on the API keys page", async () => {
    saveToken(good);
    handler = (path) => (path.startsWith("/v1/api_keys") ? Response.json({ ...empty, data: [key(), { ...key(), id: "key_71h455vb4pex5vsknk084sn02q", name: "ci", revoked_at: created }] }) : undefined);
    render(<Stub initialEntries={["/api-keys"]} />);
    await screen.findByText("(this session)");
    expect(screen.getAllByRole("button", { name: "Revoke" })).toHaveLength(1);
    expect(screen.getByText("revoked")).toBeTruthy();
  });
});

describe("errors and missing pages", () => {
  it("shows the catch-all 404 inside the shell", async () => {
    saveToken(good);
    render(<Stub initialEntries={["/does/not/exist"]} />);
    await screen.findByRole("heading", { name: "Not found" });
    expect(screen.getByText("This page does not exist.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Sign out" })).toBeTruthy();
  });

  it.each(["not-an-id", "acct_01h455vb4pex5vsknk084sn02q", "LDG_01H455VB4PEX5VSKNK084SN02Q", "ldg_81h455vb4pex5vsknk084sn02q", "..%2Fapi_keys"])(
    "rejects the ledger id %s before calling the API",
    async (id) => {
      saveToken(good);
      render(<Stub initialEntries={[`/ledgers/${id}`]} />);
      await screen.findByText("This page does not exist.");
      expect(paths().filter((p) => p !== "/v1/me")).toEqual([]);
    },
  );

  it("rejects an invalid id on an edit route before calling the API", async () => {
    saveToken(good);
    render(<Stub initialEntries={["/ledgers/ldg_bad/edit"]} />);
    await screen.findByText("This page does not exist.");
    expect(paths().filter((p) => p !== "/v1/me")).toEqual([]);
  });

  it.each([
    [404, { code: "not_found", detail: "ledger: ledger not found", request_id: "req_1" }, "Not found", "Ledger not found."],
    [403, { code: "forbidden", request_id: "req_1" }, "Not allowed", "Your API key's role does not allow this."],
    [500, { code: "internal_error", title: "Internal Server Error", request_id: "req_1" }, "Something went wrong", "Internal Server Error"],
    [409, { code: "lock_version_conflict", request_id: "req_1" }, "Something went wrong", "The account changed after it was loaded. Reload and try again."],
  ])("renders an API %i as %s", async (status, body, heading, detail) => {
    saveToken(good);
    handler = (path) => (path === `/v1/ledgers/${ledgerId}` ? Response.json(body, { status }) : undefined);
    render(<Stub initialEntries={[`/ledgers/${ledgerId}`]} />);
    await screen.findByRole("heading", { name: heading });
    expect(screen.getByText(detail)).toBeTruthy();
    expect(screen.getByText("Request req_1")).toBeTruthy();
  });

  it("renders a non-JSON server failure with its status text", async () => {
    saveToken(good);
    handler = (path) => (path.startsWith("/v1/ledgers") ? new Response("<html>", { status: 502, statusText: "Bad Gateway" }) : undefined);
    render(<Stub initialEntries={["/ledgers"]} />);
    await screen.findByRole("heading", { name: "Something went wrong" });
    expect(screen.getByText("Bad Gateway")).toBeTruthy();
  });

  it("renders a network failure as unreachable", async () => {
    saveToken(good);
    handler = (path) => {
      if (path.startsWith("/v1/ledgers")) throw new TypeError("Failed to fetch");
      return undefined;
    };
    render(<Stub initialEntries={["/ledgers"]} />);
    await screen.findByRole("heading", { name: "Cannot reach Astrum" });
    expect(screen.getByText("Check your connection and that the server is running.")).toBeTruthy();
  });
});

describe("filter bar", () => {
  it("sends filters with the currency upper-cased and offers Clear", async () => {
    saveToken(good);
    render(<Stub initialEntries={[`/ledgers/${ledgerId}?currency=usd&code=cash`]} />);
    await screen.findByText("No accounts match these filters.");
    expect(paths()).toContain(`/v1/accounts?ledger_id=${ledgerId}&code=cash&currency=USD&limit=50`);
    expect(screen.getByRole("link", { name: "Clear" }).getAttribute("href")).toBe(`/ledgers/${ledgerId}`);
    expect((screen.getByLabelText("Currency") as HTMLInputElement).value).toBe("usd");
  });

  it("has no Clear link without filters", async () => {
    saveToken(good);
    render(<Stub initialEntries={[`/ledgers/${ledgerId}`]} />);
    await screen.findByText("No accounts in this ledger yet.");
    expect(screen.queryByRole("link", { name: "Clear" })).toBeNull();
  });

  it("clears filters and resets the fields", async () => {
    saveToken(good);
    render(<Stub initialEntries={[`/ledgers/${ledgerId}?status=frozen&cursor=c1`]} />);
    await screen.findByText("No accounts match these filters.");
    expect((screen.getByLabelText("Status") as HTMLSelectElement).value).toBe("frozen");
    fireEvent.click(screen.getByRole("link", { name: "Clear" }));
    await screen.findByText("No accounts in this ledger yet.");
    expect((screen.getByLabelText("Status") as HTMLSelectElement).value).toBe("");
    expect(paths().at(-1)).toBe(`/v1/accounts?ledger_id=${ledgerId}&limit=50`);
  });

  it("treats submitted blank filters as no filters", async () => {
    saveToken(good);
    render(<Stub initialEntries={[`/ledgers/${ledgerId}?code=cash`]} />);
    await screen.findByText("No accounts match these filters.");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await screen.findByText("No accounts in this ledger yet.");
    expect(screen.queryByRole("link", { name: "Clear" })).toBeNull();
  });
});

describe("forms", () => {
  function ledgerPosts() {
    return fetchMock.mock.calls.filter(([path, init]) => String(path) === "/v1/ledgers" && init?.method === "POST");
  }

  it("rejects invalid metadata without calling the API", async () => {
    saveToken(good);
    render(<Stub initialEntries={["/ledgers/new"]} />);
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Wallets" } });
    fireEvent.change(screen.getByLabelText("Metadata"), { target: { value: "[1]" } });
    fireEvent.click(screen.getByRole("button", { name: "Create ledger" }));
    await screen.findByText('Metadata must be a JSON object, such as {"team": "payments"}.');
    expect(ledgerPosts()).toEqual([]);
  });

  it("shows a server rejection and uses a new idempotency key for the retry", async () => {
    saveToken(good);
    handler = (path, init) =>
      path === "/v1/ledgers" && init?.method === "POST" ? Response.json({ code: "validation_error", detail: "ledger: name is too long" }, { status: 422 }) : undefined;
    render(<Stub initialEntries={["/ledgers/new"]} />);
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Wallets" } });
    fireEvent.click(screen.getByRole("button", { name: "Create ledger" }));
    expect((await screen.findByRole("alert")).textContent).toBe("Name is too long.");
    fireEvent.click(screen.getByRole("button", { name: "Create ledger" }));
    await waitFor(() => expect(ledgerPosts()).toHaveLength(2));
    const [first, second] = ledgerPosts().map(([, init]) => new Headers(init?.headers).get("Idempotency-Key"));
    expect(first).toBeTruthy();
    expect(second).toBeTruthy();
    expect(second).not.toBe(first);
    expect(JSON.parse(String(ledgerPosts()[0][1]?.body))).toEqual({ name: "Wallets", description: "", metadata: {} });
  });

  it("shows a server failure in the error boundary instead of the form", async () => {
    saveToken(good);
    handler = (path, init) => (path === "/v1/ledgers" && init?.method === "POST" ? Response.json({ code: "internal_error", detail: "boom" }, { status: 500 }) : undefined);
    render(<Stub initialEntries={["/ledgers/new"]} />);
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Wallets" } });
    fireEvent.click(screen.getByRole("button", { name: "Create ledger" }));
    await screen.findByRole("heading", { name: "Something went wrong" });
    expect(screen.getByText("Boom.")).toBeTruthy();
  });

  it("saves an untouched edit form without calling PATCH", async () => {
    saveToken(good);
    render(<Stub initialEntries={[`/ledgers/${ledgerId}/edit`]} />);
    fireEvent.click(await screen.findByRole("button", { name: "Save changes" }));
    await screen.findByRole("heading", { name: "Wallets" });
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "PATCH")).toBe(false);
  });

  it("sends only the metadata diff on edit", async () => {
    saveToken(good);
    render(<Stub initialEntries={[`/ledgers/${ledgerId}/edit`]} />);
    fireEvent.change(await screen.findByLabelText("Metadata"), { target: { value: '{"team":"core","tier":1}' } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await screen.findByRole("heading", { name: "Wallets" });
    const patch = fetchMock.mock.calls.find(([, init]) => init?.method === "PATCH");
    expect(String(patch?.[0])).toBe(`/v1/ledgers/${ledgerId}`);
    expect(JSON.parse(String(patch?.[1]?.body))).toEqual({ metadata: { region: null, tier: 1 } });
  });

  it("shows an edit conflict on the form", async () => {
    saveToken(good);
    handler = (_path, init) => (init?.method === "PATCH" ? Response.json({ code: "validation_error", detail: "name is required" }, { status: 400 }) : undefined);
    render(<Stub initialEntries={[`/ledgers/${ledgerId}/edit`]} />);
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Renamed" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect((await screen.findByRole("alert")).textContent).toBe("Name is required.");
  });

  it("rejects an invalid API key expiry without calling the API", async () => {
    saveToken(good);
    render(<Stub initialEntries={["/api-keys"]} />);
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "ci" } });
    const expires = screen.getByLabelText("Expires") as HTMLInputElement;
    expires.type = "text";
    fireEvent.change(expires, { target: { value: "tomorrow" } });
    fireEvent.click(screen.getByRole("button", { name: "Create key" }));
    await screen.findByText("The expiry is not a valid date.");
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);
  });
});
