import { describe, expect, it } from "vitest";
import { toApiTime, toLocalInput } from "~/lib/time";

describe("toApiTime", () => {
  it("is null for blank or invalid input", () => {
    expect(toApiTime("")).toBeNull();
    expect(toApiTime("not a date")).toBeNull();
  });

  it("round-trips with toLocalInput", () => {
    const local = "2026-09-24T14:30";
    const iso = toApiTime(local);
    expect(iso).toMatch(/Z$/);
    expect(toLocalInput(iso)).toBe(local);
  });
});

describe("toLocalInput", () => {
  it("is blank without a time", () => {
    expect(toLocalInput(null)).toBe("");
    expect(toLocalInput("nope")).toBe("");
  });
});
