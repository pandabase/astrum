import { beforeEach, describe, expect, it } from "vitest";
import { canWrite, clearToken, isAdmin, readToken, safeNext, saveToken, signInPath } from "~/lib/session";
import type { Role } from "~/lib/types";

const origin = "http://astrum.test";

describe("safeNext edge cases", () => {
  it.each([
    ["/", "/"],
    ["/?x=1", "/?x=1"],
    ["/ledgers#top", "/ledgers#top"],
    ["/%2F%2Fevil.example", "/%2F%2Fevil.example"],
    ["/%5Cevil.example", "/%5Cevil.example"],
    ["/..//evil.example", "/..//evil.example"],
    ["/javascript:alert(1)", "/javascript:alert(1)"],
    ["/sign-in", "/sign-in"],
    ["/sign-in?next=%2Fledgers", "/sign-in?next=%2Fledgers"],
  ])("keeps the same-origin path %j", (next, want) => {
    expect(safeNext(next)).toBe(want);
  });

  it.each([
    "///evil.example",
    "//evil.example/path",
    "/\\evil.example",
    "/\\/evil.example",
    "\\\\evil.example",
    "\\/evil.example",
    "https://evil.example",
    "HTTPS://evil.example",
    "http:evil.example",
    "javascript:alert(1)",
    "JavaScript:alert(1)",
    "data:text/html,<script>alert(1)</script>",
    "%2F%2Fevil.example",
    " //evil.example",
    " /ledgers",
    "ledgers",
    "./ledgers",
    "../ledgers",
    "?next=/",
    "#frag",
  ])("sends %j home", (next) => {
    expect(safeNext(next)).toBe("/");
  });

  it.each(["/\t/evil.example", "/\n/evil.example", "/\r/evil.example", "/\t\\evil.example"])(
    "keeps %j on the same origin once a browser parses it",
    (next) => {
      expect(new URL(safeNext(next), origin).origin).toBe(origin);
    },
  );

  it("never returns a path that resolves to another origin for common attacks", () => {
    for (const next of ["//evil.example", "/\\evil.example", "https://evil.example", "javascript:alert(1)"]) {
      expect(new URL(safeNext(next), origin).origin).toBe(origin);
    }
  });
});

describe("signInPath edge cases", () => {
  it.each(["//evil.example", "https://evil.example", "", "ledgers"])("drops unsafe next %j", (next) => {
    expect(signInPath(next)).toBe("/sign-in");
  });

  it("encodes characters that would break the query", () => {
    expect(signInPath("/ledgers?a=1&b=2#x")).toBe("/sign-in?next=%2Fledgers%3Fa%3D1%26b%3D2%23x");
    expect(new URL(signInPath("/a b/ü?q=1&r=2"), origin).searchParams.get("next")).toBe("/a b/ü?q=1&r=2");
  });

  it("points back to sign-in itself without looping forever", () => {
    expect(signInPath("/sign-in")).toBe("/sign-in?next=%2Fsign-in");
  });
});

describe("roles", () => {
  it.each([
    ["admin", true, true],
    ["write", true, false],
    ["read", false, false],
  ] as const)("%s can write: %s, is admin: %s", (role, write, admin) => {
    expect(canWrite(role)).toBe(write);
    expect(isAdmin(role)).toBe(admin);
  });

  it("denies unknown roles", () => {
    expect(canWrite("owner" as Role)).toBe(false);
    expect(isAdmin("Admin" as Role)).toBe(false);
  });
});

describe("token storage", () => {
  beforeEach(() => {
    sessionStorage.clear();
    localStorage.clear();
  });

  it("is empty before sign-in", () => {
    expect(readToken()).toBeNull();
  });

  it("saves, replaces and clears the key in session storage only", () => {
    saveToken("sk_a");
    expect(readToken()).toBe("sk_a");
    expect(sessionStorage.getItem("astrum.key")).toBe("sk_a");
    expect(localStorage.length).toBe(0);
    saveToken("sk_b");
    expect(readToken()).toBe("sk_b");
    clearToken();
    expect(readToken()).toBeNull();
  });

  it("clears safely when nothing is stored", () => {
    expect(() => clearToken()).not.toThrow();
    expect(readToken()).toBeNull();
  });

  it("leaves other session values alone", () => {
    sessionStorage.setItem("other", "1");
    saveToken("sk_a");
    clearToken();
    expect(sessionStorage.getItem("other")).toBe("1");
  });
});
