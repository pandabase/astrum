import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, api, fetchAll, path } from "~/lib/api";
import { readToken, saveToken } from "~/lib/session";

function respond(status: number, body?: unknown) {
  const fetchMock = vi.fn(async (..._args: Parameters<typeof fetch>) =>
    new Response(body === undefined ? null : JSON.stringify(body), { status }),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

beforeEach(() => {
  sessionStorage.clear();
  history.replaceState(null, "", "/ledgers?page=2");
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("api", () => {
  it("sends the stored key, JSON body and idempotency key", async () => {
    saveToken("sk_stored");
    const fetchMock = respond(201, { id: "ldg_1" });

    const got = await api<{ id: string }>("/v1/ledgers", {
      method: "POST",
      body: { name: "Payments" },
      idempotencyKey: "key-1",
    });

    expect(got).toEqual({ id: "ldg_1" });
    const [path, init] = fetchMock.mock.calls[0];
    const headers = new Headers(init?.headers);
    expect(path).toBe("/v1/ledgers");
    expect(init?.method).toBe("POST");
    expect(init?.body).toBe('{"name":"Payments"}');
    expect(headers.get("Authorization")).toBe("Bearer sk_stored");
    expect(headers.get("Idempotency-Key")).toBe("key-1");
    expect(headers.get("Content-Type")).toBe("application/json");
  });

  it("turns problems into ApiError", async () => {
    saveToken("sk_stored");
    respond(422, { status: 422, code: "insufficient_funds", detail: "not enough", request_id: "req-1" });

    const err = await api("/v1/transactions").catch((e: unknown) => e);

    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({
      status: 422,
      code: "insufficient_funds",
      message: "The account does not have enough available balance.",
      requestId: "req-1",
    });
  });

  it("cleans up details for codes without their own wording", async () => {
    respond(422, { status: 422, code: "validation_error", detail: "ledger: name is required" });

    const err = await api("/v1/ledgers").catch((e: unknown) => e);

    expect(err).toMatchObject({ message: "Name is required." });
  });

  it("refuses to follow redirects", async () => {
    const fetchMock = respond(200, {});

    await api("/v1/ledgers");

    expect(fetchMock.mock.calls[0][1]?.redirect).toBe("error");
  });

  it("survives a non-JSON error body", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("bad gateway", { status: 502, statusText: "Bad Gateway" })));

    const err = await api("/v1/ledgers").catch((e: unknown) => e);

    expect(err).toMatchObject({ status: 502, code: "unknown", message: "Bad Gateway" });
  });

  it("signs out and redirects to sign-in on 401", async () => {
    saveToken("sk_revoked");
    respond(401, { code: "unauthorized" });

    const thrown = await api("/v1/ledgers").catch((e: unknown) => e);

    expect(readToken()).toBeNull();
    expect(thrown).toBeInstanceOf(Response);
    expect((thrown as Response).headers.get("Location")).toBe("/sign-in?next=%2Fledgers%3Fpage%3D2");
  });

  it("reports 401 for an explicit key without signing out", async () => {
    saveToken("sk_current");
    respond(401, { code: "unauthorized", detail: "the API key is invalid" });

    const err = await api("/v1/me", { token: "sk_candidate" }).catch((e: unknown) => e);

    expect(err).toBeInstanceOf(ApiError);
    expect(readToken()).toBe("sk_current");
  });
});

describe("path", () => {
  it("encodes interpolated values", () => {
    expect(path`/v1/ledgers/${"ldg_1"}`).toBe("/v1/ledgers/ldg_1");
    expect(path`/v1/ledgers/${"x/../api_keys"}/accounts`).toBe("/v1/ledgers/x%2F..%2Fapi_keys/accounts");
    expect(path`/v1/currencies/${undefined}`).toBe("/v1/currencies/");
  });
});

describe("fetchAll", () => {
  it("follows cursors and stops at max", async () => {
    const pages: Record<string, unknown> = {
      "": { object: "list", data: [1, 2], has_more: true, next_cursor: "c1" },
      c1: { object: "list", data: [3, 4], has_more: true, next_cursor: "c2" },
      c2: { object: "list", data: [5], has_more: false, next_cursor: null },
    };
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://x");
      return Response.json(pages[url.searchParams.get("cursor") ?? ""]);
    });
    vi.stubGlobal("fetch", fetchMock);

    expect(await fetchAll<number>("/v1/accounts?ledger_id=ldg_1")).toEqual([1, 2, 3, 4, 5]);
    expect(String(fetchMock.mock.calls[0][0])).toBe("/v1/accounts?ledger_id=ldg_1&limit=100");
    expect(await fetchAll<number>("/v1/accounts", { max: 3 })).toEqual([1, 2, 3]);
  });
});
