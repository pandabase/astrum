import { describe, expect, it } from "vitest";
import { searchParams, withQuery } from "~/lib/query";

describe("withQuery edge cases", () => {
  it("keeps zero but drops blank strings", () => {
    expect(withQuery("/v1/entries", { limit: 0, cursor: "" })).toBe("/v1/entries?limit=0");
  });

  it("keeps whitespace-only values", () => {
    expect(withQuery("/v1/accounts", { code: " " })).toBe("/v1/accounts?code=+");
  });

  it("encodes reserved and unicode characters", () => {
    const built = withQuery("/v1/accounts", { code: "a&b=c#d?e/f%g", name: "café 😀" });
    const url = new URL(built, "http://astrum.test");
    expect(url.pathname).toBe("/v1/accounts");
    expect(url.searchParams.get("code")).toBe("a&b=c#d?e/f%g");
    expect(url.searchParams.get("name")).toBe("café 😀");
    expect(url.hash).toBe("");
  });

  it("keeps parameter order", () => {
    expect(withQuery("/p", { b: 1, a: 2 })).toBe("/p?b=1&a=2");
  });

  it("returns the path for no parameters", () => {
    expect(withQuery("/p", {})).toBe("/p");
  });
});

describe("searchParams edge cases", () => {
  it("defaults everything to blank", () => {
    expect(searchParams(new Request("http://astrum.test/ledgers"), ["status"])).toEqual({ cursor: "", filters: { status: "" } });
  });

  it("reads the first of repeated values and decodes them", () => {
    const request = new Request("http://astrum.test/x?status=a&status=b&code=a%26b+c&cursor=c%2F1");
    expect(searchParams(request, ["status", "code"])).toEqual({ cursor: "c/1", filters: { status: "a", code: "a&b c" } });
  });

  it("ignores filters it was not asked for", () => {
    const request = new Request("http://astrum.test/x?secret=1&status=open");
    expect(searchParams(request, ["status"]).filters).toEqual({ status: "open" });
  });

  it("keeps empty values as blank", () => {
    expect(searchParams(new Request("http://astrum.test/x?status=&cursor="), ["status"])).toEqual({ cursor: "", filters: { status: "" } });
  });
});
