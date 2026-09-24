import { describe, expect, it } from "vitest";
import { parseImport } from "~/lib/import";

function error(text: string, max = 10) {
  const result = parseImport(text, max);
  return result.ok ? null : result.error;
}

describe("parseImport edge cases", () => {
  it.each(["", "   ", "\r\n"])("rejects the empty file %j as invalid JSON", (text) => {
    expect(error(text)).toBe("The file is not valid JSON.");
  });

  it("rejects a leading byte order mark in pasted text", () => {
    expect(error('﻿[{"a":1}]')).toBe("The file is not valid JSON.");
  });

  it("accepts a file with a byte order mark once decoded as UTF-8, as Blob.text() does", () => {
    const bytes = new Uint8Array([0xef, 0xbb, 0xbf, ...new TextEncoder().encode('[{"a":1}]')]);
    expect(parseImport(new TextDecoder().decode(bytes), 10)).toEqual({ ok: true, transactions: [{ a: 1 }] });
  });

  it("accepts CRLF line endings and surrounding whitespace", () => {
    expect(parseImport('\r\n{\r\n"transactions": [\r\n{"a": 1}\r\n]\r\n}\r\n', 10)).toEqual({ ok: true, transactions: [{ a: 1 }] });
  });

  it("ignores other fields next to transactions", () => {
    expect(parseImport('{"atomic":true,"transactions":[{}]}', 10)).toEqual({ ok: true, transactions: [{}] });
  });

  it("keeps items exactly as written", () => {
    const item = { description: "a,b", entries: [{ amount: "12345678901234567890123456789012345678" }], extra: null };
    expect(parseImport(JSON.stringify([item]), 10)).toEqual({ ok: true, transactions: [item] });
  });

  it("accepts exactly max transactions", () => {
    expect(parseImport("[{},{}]", 2).ok).toBe(true);
    expect(error("[{},{},{}]", 2)).toContain("the file has 3");
  });

  it.each([
    ["null", "Expected"],
    ["1", "Expected"],
    ['"x"', "Expected"],
    ["true", "Expected"],
    ['{"transactions":{}}', "Expected"],
    ['{"transactions":null}', "Expected"],
    ['{"Transactions":[{}]}', "Expected"],
    ['{"transactions":[]}', "no transactions"],
    ["[null]", "must be a JSON object"],
    ["[[]]", "must be a JSON object"],
    ['[{}, "x"]', "must be a JSON object"],
    ["[{},]", "not valid JSON"],
    ["a,b\n1,2", "not valid JSON"],
  ])("rejects %j", (text, message) => {
    expect(error(text)).toContain(message);
  });

  it("checks the size limit before the item shapes", () => {
    expect(error("[1,2,3]", 2)).toContain("at most 2");
  });
});
