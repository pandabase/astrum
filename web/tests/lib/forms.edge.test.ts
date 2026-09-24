import { describe, expect, it } from "vitest";
import { ApiError } from "~/lib/api";
import { descriptivePatch, formError, readDescriptive, readOriginal, text } from "~/lib/forms";

function form(values: Record<string, string | Blob>) {
  const data = new FormData();
  for (const [name, value] of Object.entries(values)) data.set(name, value);
  return data;
}

describe("text", () => {
  it("is blank for missing fields and files", () => {
    expect(text(form({}), "name")).toBe("");
    expect(text(form({ name: new File(["x"], "x.txt") }), "name")).toBe("");
  });

  it("trims whitespace and reads the first of repeated fields", () => {
    const data = form({ name: " \t a \n" });
    data.append("name", "b");
    expect(text(data, "name")).toBe("a");
  });
});

describe("readDescriptive edge cases", () => {
  it("defaults missing fields to blank and metadata to empty", () => {
    expect(readDescriptive(form({}))).toEqual({ ok: true, value: { name: "", description: "", metadata: {} } });
  });

  it("returns the metadata error unchanged", () => {
    expect(readDescriptive(form({ metadata: "{" }))).toEqual({ ok: false, error: "Metadata must be valid JSON." });
  });
});

describe("readOriginal", () => {
  it("is empty without the hidden field", () => {
    expect(readOriginal(form({}))).toEqual({});
  });

  it("parses the loaded values", () => {
    const original = { name: "a", description: "b", metadata: { c: 1 } };
    expect(readOriginal(form({ original: JSON.stringify(original) }))).toEqual(original);
  });

  it("throws on a tampered hidden field", () => {
    expect(() => readOriginal(form({ original: "{" }))).toThrow(SyntaxError);
  });
});

describe("descriptivePatch edge cases", () => {
  it("treats a missing original metadata as empty", () => {
    expect(descriptivePatch({ name: "a", description: "" } as never, { name: "a", description: "", metadata: { x: 1 } })).toEqual({
      metadata: { x: 1 },
    });
  });

  it("sends blanked text fields as empty strings", () => {
    const current = { name: "a", description: "b", metadata: {} };
    expect(descriptivePatch(current, { name: "", description: "", metadata: {} })).toEqual({ name: "", description: "" });
  });

  it("sends a nested metadata diff", () => {
    const current = { name: "a", description: "", metadata: { limits: { daily: 1, weekly: 2 } } };
    expect(descriptivePatch(current, { ...current, metadata: { limits: { daily: 1 } } })).toEqual({ metadata: { limits: { weekly: null } } });
  });

  it("is empty for an edit form submitted untouched after a round trip", () => {
    const current = { name: "Payments", description: "Main", metadata: { team: "core", n: [1, 2] } };
    const original = readOriginal(form({ original: JSON.stringify(current) }));
    const submitted = readDescriptive(form({ name: " Payments ", description: "Main", metadata: JSON.stringify(current.metadata, null, 2) }));
    expect(submitted.ok && descriptivePatch(original, submitted.value)).toEqual({});
  });
});

describe("formError edge cases", () => {
  it.each([400, 404, 409, 413, 422])("shows %i as a form message", (status) => {
    expect(formError(new ApiError(status, { detail: "bad thing" }))).toEqual({ error: "Bad thing." });
  });

  it.each([401, 429, 500, 502, 503])("rethrows %i", (status) => {
    expect(() => formError(new ApiError(status, { detail: "x" }))).toThrow(ApiError);
  });

  it("uses the role message for every 403 regardless of detail", () => {
    expect(formError(new ApiError(403, { code: "insufficient_funds", detail: "x" }))).toEqual({ error: "Your API key's role does not allow this." });
  });

  it("rethrows non-errors", () => {
    expect(() => formError("boom")).toThrow("boom");
    const response = new Response(null, { status: 302 });
    expect(() => formError(response)).toThrow();
  });
});
