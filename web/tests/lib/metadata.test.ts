import { describe, expect, it } from "vitest";
import { formatMetadata, mergePatch, parseMetadata } from "~/lib/metadata";

describe("parseMetadata", () => {
  it("treats blank as empty", () => {
    expect(parseMetadata("  ")).toEqual({ ok: true, value: {} });
  });

  it("accepts an object", () => {
    expect(parseMetadata('{"team":"payments","limits":{"daily":5}}')).toEqual({
      ok: true,
      value: { team: "payments", limits: { daily: 5 } },
    });
  });

  it.each(["{", "[1]", '"text"', "null", "3"])("rejects %s", (text) => {
    expect(parseMetadata(text).ok).toBe(false);
  });
});

describe("mergePatch", () => {
  it("is empty when nothing changed", () => {
    expect(mergePatch({ a: 1, b: { c: [1, 2] } }, { a: 1, b: { c: [1, 2] } })).toEqual({});
  });

  it("adds, replaces and removes keys", () => {
    expect(mergePatch({ keep: 1, change: "a", drop: true }, { keep: 1, change: "b", add: 2 })).toEqual({
      change: "b",
      drop: null,
      add: 2,
    });
  });

  it("diffs nested objects so removed nested keys are removed", () => {
    expect(mergePatch({ limits: { daily: 5, weekly: 20 } }, { limits: { daily: 10 } })).toEqual({
      limits: { daily: 10, weekly: null },
    });
  });

  it("replaces arrays and type changes whole", () => {
    expect(mergePatch({ tags: ["a"], x: { y: 1 } }, { tags: ["a", "b"], x: 5 })).toEqual({ tags: ["a", "b"], x: 5 });
  });

  it("clears everything", () => {
    expect(mergePatch({ a: 1, b: 2 }, {})).toEqual({ a: null, b: null });
  });
});

describe("formatMetadata", () => {
  it("is blank for empty metadata", () => {
    expect(formatMetadata({})).toBe("");
    expect(formatMetadata(null)).toBe("");
  });

  it("pretty prints", () => {
    expect(formatMetadata({ a: 1 })).toBe('{\n  "a": 1\n}');
  });
});
