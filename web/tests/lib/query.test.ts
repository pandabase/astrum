import { describe, expect, it } from "vitest";
import { searchParams, withQuery } from "~/lib/query";

describe("withQuery", () => {
  it("leaves out empty values", () => {
    expect(withQuery("/v1/accounts", { ledger_id: "ldg_1", status: "", cursor: null, limit: 50 })).toBe(
      "/v1/accounts?ledger_id=ldg_1&limit=50",
    );
  });

  it("returns the bare path without values", () => {
    expect(withQuery("/v1/ledgers", { cursor: undefined })).toBe("/v1/ledgers");
  });
});

describe("searchParams", () => {
  it("reads the cursor and filters, defaulting to blank", () => {
    const request = new Request("http://astrum.test/ledgers/ldg_1?status=frozen&cursor=abc");
    expect(searchParams(request, ["status", "currency"])).toEqual({
      cursor: "abc",
      filters: { status: "frozen", currency: "" },
    });
  });
});
