import { afterEach, describe, expect, it, vi } from "vitest";
import { accountsById, ledgerAccounts } from "~/lib/accounts";
import { ApiError } from "~/lib/api";
import { retryDelivery } from "~/lib/deliveries";
import { eventGroups, eventTypes } from "~/lib/event-types";
import { monitorFields, monitorOperators } from "~/lib/monitors";
import type { Account } from "~/lib/types";

afterEach(() => {
  vi.unstubAllGlobals();
});

function form(values: Record<string, string>) {
  const data = new FormData();
  for (const [name, value] of Object.entries(values)) data.set(name, value);
  return data;
}

const deliveryId = "wd_01h455vb4pex5vsknk084sn02q";

describe("retryDelivery", () => {
  it.each<Record<string, string>>([{}, { delivery_id: "" }, { delivery_id: "we_01h455vb4pex5vsknk084sn02q" }, { delivery_id: "wd_x/../retry" }])(
    "rejects %j without calling the API",
    async (values) => {
      const fetchMock = vi.fn();
      vi.stubGlobal("fetch", fetchMock);
      expect(await retryDelivery(form(values), new AbortController().signal)).toEqual({ error: "Unknown delivery." });
      expect(fetchMock).not.toHaveBeenCalled();
    },
  );

  it("posts to the retry endpoint and returns null", async () => {
    const fetchMock = vi.fn(async (..._args: Parameters<typeof fetch>) => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    expect(await retryDelivery(form({ delivery_id: ` ${deliveryId} ` }), new AbortController().signal)).toBeNull();
    expect(fetchMock.mock.calls[0][0]).toBe(`/v1/webhook_deliveries/${deliveryId}/retry`);
    expect(fetchMock.mock.calls[0][1]?.method).toBe("POST");
  });

  it("shows a rejected retry as an error", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => Response.json({ code: "not_found", detail: "webhook: delivery not found" }, { status: 404 })));
    expect(await retryDelivery(form({ delivery_id: deliveryId }), new AbortController().signal)).toEqual({ error: "Delivery not found." });
  });

  it("rethrows server failures", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => Response.json({ code: "internal_error" }, { status: 500 })));
    await expect(retryDelivery(form({ delivery_id: deliveryId }), new AbortController().signal)).rejects.toBeInstanceOf(ApiError);
  });
});

describe("event types", () => {
  it("are unique dotted names", () => {
    expect(new Set(eventTypes).size).toBe(eventTypes.length);
    for (const type of eventTypes) expect(type).toMatch(/^[a-z_]+\.[a-z_]+$/);
  });

  it("have groups that each cover at least one type and are not types themselves", () => {
    for (const group of eventGroups) {
      expect(group).toMatch(/^[a-z_]+\.\*$/);
      const prefix = group.slice(0, -1);
      expect(eventTypes.some((type) => type.startsWith(prefix))).toBe(true);
      expect(eventTypes as readonly string[]).not.toContain(group);
    }
  });
});

describe("monitor labels", () => {
  it("cover every field and operator with distinct labels", () => {
    expect(Object.keys(monitorFields).sort()).toEqual(["available_balance_amount", "pending_balance_amount", "posted_balance_amount"]);
    expect(Object.keys(monitorOperators).sort()).toEqual(["eq", "gt", "gte", "lt", "lte", "not_eq"]);
    expect(new Set(Object.values(monitorFields)).size).toBe(3);
    expect(new Set(Object.values(monitorOperators)).size).toBe(6);
  });
});

let seq = 0;
function id() {
  seq++;
  return `acct_0${String(seq).padStart(25, "0")}`;
}

function account(accountId: string, code: string): Account {
  return { id: accountId, code, currency: "USD", currency_exponent: 2 } as Account;
}

describe("accountsById", () => {
  it("dedupes ids and skips blanks without calling the API for them", async () => {
    const a = id();
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => Response.json(account(String(input).split("/").pop()!, "A")));
    vi.stubGlobal("fetch", fetchMock);
    const got = await accountsById([a, a, null, undefined, ""]);
    expect([...got.keys()]).toEqual([a]);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("caches accounts for later lookups", async () => {
    const a = id();
    const fetchMock = vi.fn(async () => Response.json(account(a, "A")));
    vi.stubGlobal("fetch", fetchMock);
    await accountsById([a]);
    const again = await accountsById([a]);
    expect(again.get(a)?.code).toBe("A");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("leaves out ids that fail and retries them later", async () => {
    const a = id();
    const b = id();
    let fail = true;
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const wanted = String(input).split("/").pop()!;
      if (wanted === b && fail) return Response.json({ code: "not_found" }, { status: 404 });
      return Response.json(account(wanted, wanted));
    });
    vi.stubGlobal("fetch", fetchMock);
    expect([...(await accountsById([a, b])).keys()]).toEqual([a]);
    fail = false;
    expect([...(await accountsById([b, a])).keys()]).toEqual([b, a]);
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("swallows network errors", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => { throw new TypeError("offline"); }));
    expect((await accountsById([id()])).size).toBe(0);
  });

  it("encodes ids in the request path", async () => {
    const fetchMock = vi.fn(async () => Response.json({ code: "not_found" }, { status: 404 }));
    vi.stubGlobal("fetch", fetchMock);
    await accountsById(["x/../api_keys"]);
    expect(fetchMock.mock.calls[0]).toContain("/v1/accounts/x%2F..%2Fapi_keys");
  });

  it("returns an empty map for no ids", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect((await accountsById([])).size).toBe(0);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("ledgerAccounts", () => {
  it("sorts by code and fills the cache", async () => {
    const [a, b, c] = [id(), id(), id()];
    const fetchMock = vi.fn(async (..._args: Parameters<typeof fetch>) =>
      Response.json({ object: "list", data: [account(a, "zeta"), account(b, "alpha"), account(c, "Beta")], has_more: false, next_cursor: null }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const got = await ledgerAccounts("ldg_1");
    expect(got.map((x) => x.id)).toEqual([b, c, a]);
    expect(String(fetchMock.mock.calls[0][0])).toBe("/v1/accounts?ledger_id=ldg_1&limit=100");
    await accountsById([a, b, c]);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
