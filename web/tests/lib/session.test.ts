import { describe, expect, it } from "vitest";
import { safeNext, signInPath } from "~/lib/session";

describe("safeNext", () => {
  it.each([
    [null, "/"],
    ["", "/"],
    ["/ledgers", "/ledgers"],
    ["/accounts/acct_1?tab=entries", "/accounts/acct_1?tab=entries"],
    ["https://evil.example", "/"],
    ["//evil.example", "/"],
    ["/\\evil.example", "/"],
    ["ledgers", "/"],
  ])("maps %j to %j", (next, want) => {
    expect(safeNext(next)).toBe(want);
  });
});

describe("signInPath", () => {
  it("omits next for the home page", () => {
    expect(signInPath("/")).toBe("/sign-in");
  });

  it("encodes the page to return to", () => {
    expect(signInPath("/ledgers?page=2")).toBe("/sign-in?next=%2Fledgers%3Fpage%3D2");
  });
});
