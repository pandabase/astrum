import { describe, expect, it } from "vitest";
import { parseImport } from "~/lib/import";

describe("parseImport", () => {
  it("accepts the API body or a bare array", () => {
    expect(parseImport('{"transactions":[{"entries":[]}]}', 10)).toEqual({ ok: true, transactions: [{ entries: [] }] });
    expect(parseImport('[{"a":1},{"b":2}]', 10)).toEqual({ ok: true, transactions: [{ a: 1 }, { b: 2 }] });
  });

  it.each([
    ["{", "not valid JSON"],
    ['{"items":[]}', "Expected"],
    ["[]", "no transactions"],
    ["[1]", "must be a JSON object"],
    ['[{},{},{}]', "at most 2"],
  ])("rejects %s", (text, message) => {
    const result = parseImport(text, 2);
    expect(!result.ok && result.error).toContain(message);
  });
});
