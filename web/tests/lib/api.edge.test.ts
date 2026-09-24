import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, api, errorMessage, fetchAll, newIdempotencyKey, path } from "~/lib/api";
import { readToken, saveToken } from "~/lib/session";

type Init = RequestInit | undefined;

function stub(handler: (input: string, init: Init) => Response | Promise<Response>) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => handler(String(input), init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function headers(fetchMock: ReturnType<typeof stub>, call = 0) {
  return new Headers(fetchMock.mock.calls[call][1]?.headers);
}

beforeEach(() => {
  sessionStorage.clear();
  history.replaceState(null, "", "/ledgers");
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("api request", () => {
  it("sends no Authorization header when signed out", async () => {
    const fetchMock = stub(() => Response.json({}));
    await api("/v1/me");
    expect(headers(fetchMock).has("Authorization")).toBe(false);
    expect(headers(fetchMock).get("Accept")).toBe("application/json");
  });

  it("prefers an explicit key over the stored one", async () => {
    saveToken("sk_stored");
    const fetchMock = stub(() => Response.json({}));
    await api("/v1/me", { token: "sk_explicit" });
    expect(headers(fetchMock).get("Authorization")).toBe("Bearer sk_explicit");
  });

  it("sends no Authorization header for an empty explicit key", async () => {
    saveToken("sk_stored");
    const fetchMock = stub(() => Response.json({}));
    await api("/v1/me", { token: "" });
    expect(headers(fetchMock).has("Authorization")).toBe(false);
  });

  it("defaults to GET without body, content type or idempotency key", async () => {
    const fetchMock = stub(() => Response.json({}));
    await api("/v1/ledgers", { idempotencyKey: "" });
    const init = fetchMock.mock.calls[0][1];
    expect(init?.method).toBe("GET");
    expect(init?.body).toBeUndefined();
    expect(headers(fetchMock).has("Content-Type")).toBe(false);
    expect(headers(fetchMock).has("Idempotency-Key")).toBe(false);
    expect(init?.redirect).toBe("error");
  });

  it.each([
    [null, "null"],
    [0, "0"],
    [false, "false"],
    ["", '""'],
    [[], "[]"],
  ])("serializes the falsy body %j", async (body, want) => {
    const fetchMock = stub(() => Response.json({}));
    await api("/v1/x", { method: "POST", body });
    expect(fetchMock.mock.calls[0][1]?.body).toBe(want);
    expect(headers(fetchMock).get("Content-Type")).toBe("application/json");
  });

  it("keeps large amounts as strings in the body", async () => {
    const fetchMock = stub(() => Response.json({}));
    await api("/v1/x", { method: "POST", body: { amount: "99999999999999999999999999999999999999" } });
    expect(fetchMock.mock.calls[0][1]?.body).toBe('{"amount":"99999999999999999999999999999999999999"}');
  });

  it("passes the abort signal through", async () => {
    const controller = new AbortController();
    const fetchMock = stub(() => Response.json({}));
    await api("/v1/x", { signal: controller.signal });
    expect(fetchMock.mock.calls[0][1]?.signal).toBe(controller.signal);
  });

  it("uses the same-origin path unchanged", async () => {
    const fetchMock = stub(() => Response.json({}));
    await api("/v1/accounts?ledger_id=ldg_1&limit=100");
    expect(fetchMock.mock.calls[0][0]).toBe("/v1/accounts?ledger_id=ldg_1&limit=100");
  });
});

describe("api responses", () => {
  it("returns undefined for 204 without reading the body", async () => {
    stub(() => new Response(null, { status: 204 }));
    await expect(api("/v1/x", { method: "DELETE" })).resolves.toBeUndefined();
  });

  it("rejects an empty 200 body", async () => {
    stub(() => new Response("", { status: 200 }));
    await expect(api("/v1/x")).rejects.toThrow(SyntaxError);
  });

  it("returns JSON null and arrays as is", async () => {
    stub(() => new Response("null", { status: 200 }));
    await expect(api("/v1/x")).resolves.toBeNull();
    stub(() => Response.json([1, 2]));
    await expect(api("/v1/x")).resolves.toEqual([1, 2]);
  });

  it("propagates network failures as TypeError", async () => {
    stub(() => {
      throw new TypeError("Failed to fetch");
    });
    await expect(api("/v1/x")).rejects.toThrow(TypeError);
  });

  it("propagates aborts", async () => {
    stub(() => {
      throw new DOMException("aborted", "AbortError");
    });
    await expect(api("/v1/x")).rejects.toMatchObject({ name: "AbortError" });
  });

  it("uses the title when there is neither a known code nor a detail", async () => {
    stub(() => Response.json({ title: "Conflict", code: "something_new" }, { status: 409 }));
    await expect(api("/v1/x")).rejects.toMatchObject({ status: 409, code: "something_new", message: "Conflict" });
  });

  it("falls back to the status when the problem is empty", async () => {
    stub(() => Response.json({}, { status: 500 }));
    const err = await api("/v1/x").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 500, code: "unknown", message: "Request failed with status 500.", requestId: undefined });
    expect((err as ApiError).name).toBe("ApiError");
  });

  it("prefers the friendly message over detail and title", async () => {
    stub(() => Response.json({ title: "T", code: "lock_version_conflict", detail: "d" }, { status: 409 }));
    await expect(api("/v1/x")).rejects.toMatchObject({ message: "The account changed after it was loaded. Reload and try again." });
  });

  it("falls back to the status for a non-JSON body without status text", async () => {
    stub(() => new Response("<html>oops</html>", { status: 502 }));
    await expect(api("/v1/x")).rejects.toMatchObject({ status: 502, code: "unknown", message: "Request failed with status 502." });
  });

  it("falls back to the status for a JSON null error body", async () => {
    stub(() => new Response("null", { status: 500 }));
    const err = await api("/v1/x").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 500, message: "Request failed with status 500." });
  });

  it("treats a 401 with a non-JSON body as sign-out too", async () => {
    saveToken("sk_x");
    stub(() => new Response("nope", { status: 401 }));
    const thrown = await api("/v1/x").catch((e: unknown) => e);
    expect(thrown).toBeInstanceOf(Response);
    expect(readToken()).toBeNull();
  });

  it("redirects to plain sign-in from the home page", async () => {
    history.replaceState(null, "", "/");
    stub(() => new Response(null, { status: 401 }));
    const thrown = (await api("/v1/x").catch((e: unknown) => e)) as Response;
    expect(thrown.headers.get("Location")).toBe("/sign-in");
  });

  it("does not sign out on 403", async () => {
    saveToken("sk_read");
    stub(() => Response.json({ code: "forbidden", detail: "the key's role does not allow this" }, { status: 403 }));
    await expect(api("/v1/api_keys")).rejects.toMatchObject({ status: 403 });
    expect(readToken()).toBe("sk_read");
  });
});

describe("errorMessage", () => {
  it("is null without code or detail", () => {
    expect(errorMessage(undefined, undefined)).toBeNull();
    expect(errorMessage("brand_new_code", undefined)).toBeNull();
    expect(errorMessage("brand_new_code", "")).toBeNull();
  });

  it.each([
    ["ledger: account: name is required", "Name is required."],
    ["ledger: ", null],
    ["Ledger: bad", "Ledger: bad."],
    ["balance_lock: failed", "Balance_lock: failed."],
    ["done.", "Done."],
    ["really?", "Really?"],
    ["stop!", "Stop!"],
    ["ends with colon:", "Ends with colon:."],
    ["ünicode start", "Ünicode start."],
    ["a", "A."],
    ["amount 1.50 exceeds limit: 1.00", "Amount 1.50 exceeds limit: 1.00."],
  ])("cleans %j to %j", (detail, want) => {
    expect(errorMessage("brand_new_code", detail)).toBe(want);
  });

  it("maps every known code regardless of detail", () => {
    for (const code of [
      "account_exists",
      "account_not_empty",
      "balance_lock_failed",
      "account_not_open",
      "currency_exists",
      "idempotency_key_in_use",
      "idempotency_key_reused",
      "insufficient_funds",
      "lock_version_conflict",
      "unbalanced_transaction",
      "unknown_currency",
      "unknown_ledger",
    ]) {
      const message = errorMessage(code, "raw detail");
      expect(message).not.toBe("Raw detail.");
      expect(message).toMatch(/^[A-Z].*\.$/);
    }
  });

  it("does not treat codes named like object members as known", () => {
    expect(errorMessage("constructor", "x")).toBe("X.");
    expect(errorMessage("toString", undefined)).toBeNull();
  });
});

describe("path edge cases", () => {
  it.each([
    ["a/b", "a%2Fb"],
    ["a?b=c", "a%3Fb%3Dc"],
    ["a#b", "a%23b"],
    ["100%", "100%25"],
    ["..", ".."],
    [".", "."],
    ["a b", "a%20b"],
    ["café", "caf%C3%A9"],
    ["😀", "%F0%9F%98%80"],
    ["a&b", "a%26b"],
    ["\\", "%5C"],
    ["", ""],
  ])("encodes %j as %j", (value, want) => {
    expect(path`/v1/ledgers/${value}`).toBe(`/v1/ledgers/${want}`);
  });

  it("keeps a lone dot-dot segment, which fetch would resolve upward", () => {
    expect(new URL(path`/v1/ledgers/${".."}`, "http://astrum.test").pathname).toBe("/v1/");
  });

  it("encodes several values and leaves the template alone", () => {
    expect(path`/v1/${"a/b"}/x/${"c?d"}?keep=1`).toBe("/v1/a%2Fb/x/c%3Fd?keep=1");
    expect(path`/v1/plain`).toBe("/v1/plain");
  });
});

describe("newIdempotencyKey", () => {
  it("returns a fresh UUID each time", () => {
    const a = newIdempotencyKey();
    expect(a).toMatch(/^[0-9a-f-]{36}$/);
    expect(newIdempotencyKey()).not.toBe(a);
  });
});

describe("fetchAll edge cases", () => {
  function pages(list: Record<string, { data: number[]; next_cursor: string | null }>) {
    return stub((input) => {
      const cursor = new URL(input, "http://x").searchParams.get("cursor") ?? "";
      const page = list[cursor];
      return Response.json({ object: "list", has_more: page.next_cursor !== null, ...page });
    });
  }

  it("makes one request for a single page", async () => {
    const fetchMock = pages({ "": { data: [1], next_cursor: null } });
    expect(await fetchAll("/v1/x")).toEqual([1]);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("returns empty for an empty list", async () => {
    pages({ "": { data: [], next_cursor: null } });
    expect(await fetchAll("/v1/x")).toEqual([]);
  });

  it("stops at an empty-string cursor", async () => {
    const fetchMock = pages({ "": { data: [1], next_cursor: "" } });
    expect(await fetchAll("/v1/x")).toEqual([1]);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("overrides a limit and cursor already in the path", async () => {
    const fetchMock = pages({ stale: { data: [1], next_cursor: null } });
    await fetchAll("/v1/x?limit=5&cursor=stale&a=1");
    const url = new URL(String(fetchMock.mock.calls[0][0]), "http://x");
    expect(url.searchParams.getAll("limit")).toEqual(["100"]);
    expect(url.searchParams.get("a")).toBe("1");
    expect(url.searchParams.get("cursor")).toBe("stale");
  });

  it("encodes cursors with reserved characters", async () => {
    const fetchMock = pages({ "": { data: [1], next_cursor: "a+b/c=&" }, "a+b/c=&": { data: [2], next_cursor: null } });
    expect(await fetchAll("/v1/x")).toEqual([1, 2]);
    expect(String(fetchMock.mock.calls[1][0])).toContain("cursor=a%2Bb%2Fc%3D%26");
  });

  it("stops requesting once max is reached", async () => {
    const fetchMock = pages({
      "": { data: [1, 2], next_cursor: "c1" },
      c1: { data: [3, 4], next_cursor: "c2" },
      c2: { data: [5], next_cursor: null },
    });
    expect(await fetchAll("/v1/x", { max: 2 })).toEqual([1, 2]);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("returns nothing for max 0 after one request", async () => {
    const fetchMock = pages({ "": { data: [1], next_cursor: "c1" }, c1: { data: [2], next_cursor: null } });
    expect(await fetchAll("/v1/x", { max: 0 })).toEqual([]);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("defaults to at most 1000 items", async () => {
    let n = 0;
    const fetchMock = stub(() => {
      const data = Array.from({ length: 100 }, () => n++);
      return Response.json({ object: "list", data, has_more: true, next_cursor: `c${n}` });
    });
    const items = await fetchAll<number>("/v1/x");
    expect(items).toHaveLength(1000);
    expect(fetchMock).toHaveBeenCalledTimes(10);
  });

  it("is bounded by max when the server repeats a cursor", async () => {
    const fetchMock = stub(() => Response.json({ object: "list", data: [1], has_more: true, next_cursor: "same" }));
    expect(await fetchAll("/v1/x", { max: 5 })).toHaveLength(5);
    expect(fetchMock).toHaveBeenCalledTimes(5);
  });

  it("follows next_cursor even when has_more is false", async () => {
    const fetchMock = stub((input) =>
      Response.json(
        input.includes("cursor=")
          ? { object: "list", data: [2], has_more: false, next_cursor: null }
          : { object: "list", data: [1], has_more: false, next_cursor: "c1" },
      ),
    );
    expect(await fetchAll("/v1/x")).toEqual([1, 2]);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("propagates an error from a later page", async () => {
    stub((input) =>
      input.includes("cursor=")
        ? Response.json({ code: "internal_error" }, { status: 500 })
        : Response.json({ object: "list", data: [1], has_more: true, next_cursor: "c1" }),
    );
    await expect(fetchAll("/v1/x")).rejects.toBeInstanceOf(ApiError);
  });
});
