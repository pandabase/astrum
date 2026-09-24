import { describe, expect, it } from "vitest";
import { formatMetadata, mergePatch, parseMetadata } from "~/lib/metadata";

describe("parseMetadata edge cases", () => {
  it.each(["", "\n\t ", " "])("treats %j as empty", (text) => {
    expect(parseMetadata(text)).toEqual({ ok: true, value: {} });
  });

  it("accepts an empty object and surrounding whitespace", () => {
    expect(parseMetadata(" \r\n{}\r\n ")).toEqual({ ok: true, value: {} });
    expect(parseMetadata('\r\n{\r\n  "a": 1\r\n}\r\n')).toEqual({ ok: true, value: { a: 1 } });
  });

  it.each(["true", "false", '""', "[]", "[{}]", "1e3", "{a:1}", "{'a':1}", '{"a":1,}', '{"a":1} {"b":2}', "undefined", "NaN", "﻿{}"])(
    "rejects %j",
    (text) => {
      expect(parseMetadata(text).ok).toBe(false);
    },
  );

  it("explains the difference between invalid JSON and a non-object", () => {
    expect(parseMetadata("{")).toEqual({ ok: false, error: "Metadata must be valid JSON." });
    expect(parseMetadata("[]")).toMatchObject({ ok: false, error: expect.stringContaining("JSON object") });
  });

  it("keeps the last value of a duplicate key rather than rejecting it", () => {
    expect(parseMetadata('{"a":1,"a":2}')).toEqual({ ok: true, value: { a: 2 } });
  });

  it("loses precision on numbers beyond 2^53", () => {
    const parsed = parseMetadata('{"n":12345678901234567890123,"id":9007199254740993}');
    expect(parsed).toEqual({ ok: true, value: { n: 1.2345678901234568e22, id: 9007199254740992 } });
  });

  it("keeps large numbers written as strings", () => {
    expect(parseMetadata('{"n":"12345678901234567890123"}')).toEqual({ ok: true, value: { n: "12345678901234567890123" } });
  });

  it("keeps unicode and escaped characters", () => {
    expect(parseMetadata('{"k\\u00e9y":"\\ud83d\\ude00","emoji":"😀"}')).toEqual({ ok: true, value: { "kéy": "😀", emoji: "😀" } });
  });

  it("keeps a __proto__ key as data instead of a prototype", () => {
    const parsed = parseMetadata('{"__proto__":{"polluted":true}}');
    expect(parsed.ok && Object.keys(parsed.value)).toEqual(["__proto__"]);
    expect(({} as Record<string, unknown>).polluted).toBeUndefined();
  });
});

describe("formatMetadata edge cases", () => {
  it("is blank for undefined", () => {
    expect(formatMetadata(undefined)).toBe("");
  });

  it("round-trips through parseMetadata", () => {
    const metadata = { team: "core", nested: { list: [1, "two", null], empty: {} }, flag: false, none: null };
    const text = formatMetadata(metadata);
    expect(parseMetadata(text)).toEqual({ ok: true, value: metadata });
  });

  it("prints metadata whose only value is null", () => {
    expect(formatMetadata({ a: null })).toBe('{\n  "a": null\n}');
  });
});

describe("mergePatch edge cases", () => {
  it("is empty for two empty objects", () => {
    expect(mergePatch({}, {})).toEqual({});
  });

  it("ignores key order", () => {
    expect(mergePatch({ a: 1, b: { c: 1, d: 2 } }, { b: { d: 2, c: 1 }, a: 1 })).toEqual({});
  });

  it("adds keys to empty metadata", () => {
    expect(mergePatch({}, { a: 1, b: { c: 2 } })).toEqual({ a: 1, b: { c: 2 } });
  });

  it("diffs deeply nested objects", () => {
    expect(mergePatch({ a: { b: { c: 1, d: 2 }, e: 3 } }, { a: { b: { c: 1, d: 5 }, e: 3 } })).toEqual({ a: { b: { d: 5 } } });
  });

  it("leaves unchanged nested objects out", () => {
    expect(mergePatch({ a: { b: 1 }, c: 1 }, { a: { b: 1 }, c: 2 })).toEqual({ c: 2 });
  });

  it("removes every key of a nested object emptied in place", () => {
    expect(mergePatch({ a: { b: 1, c: 2 } }, { a: {} })).toEqual({ a: { b: null, c: null } });
  });

  it("removes a nested object entirely", () => {
    expect(mergePatch({ a: { b: 1 } }, {})).toEqual({ a: null });
  });

  it("replaces an object with a scalar, array or null and back", () => {
    expect(mergePatch({ a: { b: 1 } }, { a: [1] })).toEqual({ a: [1] });
    expect(mergePatch({ a: [1] }, { a: { b: 1 } })).toEqual({ a: { b: 1 } });
    expect(mergePatch({ a: "x" }, { a: { b: 1 } })).toEqual({ a: { b: 1 } });
    expect(mergePatch({ a: { b: 1 } }, { a: null })).toEqual({ a: null });
  });

  it("replaces arrays whole, even when only an element changed", () => {
    expect(mergePatch({ a: [1, 2, 3] }, { a: [1, 2] })).toEqual({ a: [1, 2] });
    expect(mergePatch({ a: [{ x: 1, y: 2 }] }, { a: [{ x: 1, y: 3 }] })).toEqual({ a: [{ x: 1, y: 3 }] });
    expect(mergePatch({ a: [] }, { a: [] })).toEqual({});
  });

  it("resends an array whose objects only changed key order", () => {
    expect(mergePatch({ a: [{ x: 1, y: 2 }] }, { a: [{ y: 2, x: 1 }] })).toEqual({ a: [{ y: 2, x: 1 }] });
  });

  it("distinguishes values with the same loose meaning", () => {
    expect(mergePatch({ a: 1 }, { a: "1" })).toEqual({ a: "1" });
    expect(mergePatch({ a: 0 }, { a: false })).toEqual({ a: false });
    expect(mergePatch({ a: "" }, { a: null })).toEqual({ a: null });
  });

  it("cannot store an explicit null, since null in a merge patch removes the key", () => {
    expect(mergePatch({}, { a: null })).toEqual({ a: null });
    expect(mergePatch({ a: null }, { a: null })).toEqual({});
  });

  it("does not mutate its inputs", () => {
    const before = { a: { b: 1 }, c: 1 };
    const after = { a: { b: 2 } };
    mergePatch(before, after);
    expect(before).toEqual({ a: { b: 1 }, c: 1 });
    expect(after).toEqual({ a: { b: 2 } });
  });

  it("produces a patch that turns before into after when applied", () => {
    const before = { a: 1, b: { c: 2, d: { e: 3 } }, f: [1], g: "x" };
    const after = { a: 2, b: { d: { e: 4, h: 5 } }, f: [2], i: true };
    expect(apply(before, mergePatch(before, after))).toEqual(after);
  });

  it.each(["constructor", "toString", "hasOwnProperty", "valueOf"])("removes a key named %s", (name) => {
    expect(mergePatch({ [name]: "x" }, {})).toEqual({ [name]: null });
  });

  it("keeps a changed __proto__ key in the patch", () => {
    const after = parseMetadata('{"__proto__":{"a":1}}');
    expect(after.ok).toBe(true);
    const patch = mergePatch({}, after.ok ? after.value : {});
    expect(JSON.stringify(patch)).toBe('{"__proto__":{"a":1}}');
  });
});

function apply(target: Record<string, unknown>, patch: Record<string, unknown>): Record<string, unknown> {
  const out = { ...target };
  for (const [name, value] of Object.entries(patch)) {
    if (value === null) delete out[name];
    else if (typeof value === "object" && !Array.isArray(value)) {
      const prev = out[name];
      out[name] = apply(typeof prev === "object" && prev !== null && !Array.isArray(prev) ? (prev as Record<string, unknown>) : {}, value as Record<string, unknown>);
    } else out[name] = value;
  }
  return out;
}
