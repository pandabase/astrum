import { describe, expect, it } from "vitest";
import { ApiError } from "~/lib/api";
import { descriptivePatch, formError, readDescriptive } from "~/lib/forms";

function form(values: Record<string, string>) {
  const data = new FormData();
  for (const [name, value] of Object.entries(values)) data.set(name, value);
  return data;
}

describe("readDescriptive", () => {
  it("trims text and parses metadata", () => {
    expect(readDescriptive(form({ name: " Payments ", description: "", metadata: '{"team":"core"}' }))).toEqual({
      ok: true,
      value: { name: "Payments", description: "", metadata: { team: "core" } },
    });
  });

  it("reports bad metadata", () => {
    expect(readDescriptive(form({ name: "x", metadata: "[1]" })).ok).toBe(false);
  });
});

describe("descriptivePatch", () => {
  const current = { name: "Payments", description: "Main", metadata: { team: "core", region: "eu" } };

  it("sends only what changed", () => {
    expect(descriptivePatch(current, { ...current, description: "Primary", metadata: { team: "core" } })).toEqual({
      description: "Primary",
      metadata: { region: null },
    });
  });

  it("is empty when nothing changed", () => {
    expect(descriptivePatch(current, { ...current, metadata: { region: "eu", team: "core" } })).toEqual({});
  });
});

describe("formError", () => {
  it("returns messages for rejected input", () => {
    expect(formError(new ApiError(422, { code: "validation_error", detail: "name is required" }))).toEqual({
      error: "Name is required.",
    });
    expect(formError(new ApiError(403, { code: "forbidden" }))).toEqual({
      error: "Your API key's role does not allow this.",
    });
  });

  it("rethrows server failures and other errors", () => {
    expect(() => formError(new ApiError(500, { code: "internal_error" }))).toThrow(ApiError);
    expect(() => formError(new TypeError("offline"))).toThrow(TypeError);
  });
});
