import { describe, expect, it } from "vitest";
import { formatAmount, parseAmount } from "~/lib/format";

describe("formatAmount", () => {
  it.each([
    ["0", 2, "0.00"],
    ["5", 2, "0.05"],
    ["100", 2, "1.00"],
    ["123456789", 2, "1,234,567.89"],
    ["-150", 2, "-1.50"],
    ["-0", 2, "0.00"],
    ["007", 2, "0.07"],
    ["1000", 0, "1,000"],
    ["500000000000000000", 18, "0.500000000000000000"],
    ["99999999999999999999999999999999999999", 2, "999,999,999,999,999,999,999,999,999,999,999,999.99"],
  ])("formats %s with exponent %i as %s", (minor, exponent, want) => {
    expect(formatAmount(minor, exponent)).toBe(want);
  });

  it.each(["", "1.5", "1e3", "abc", "--1"])("rejects %j", (minor) => {
    expect(() => formatAmount(minor, 2)).toThrow();
  });
});

describe("parseAmount", () => {
  it.each([
    ["1", 2, "100"],
    ["1.5", 2, "150"],
    ["1,234.56", 2, "123456"],
    ["0.07", 2, "7"],
    ["-2.50", 2, "-250"],
    ["-0", 2, "0"],
    [" 12 ", 0, "12"],
    ["1.", 2, "100"],
    ["0.5", 18, "500000000000000000"],
  ])("parses %j with exponent %i as %s", (input, exponent, want) => {
    expect(parseAmount(input, exponent)).toBe(want);
  });

  it.each([
    ["", 2],
    ["abc", 2],
    ["1.234", 2],
    ["1.5", 0],
    ["1e3", 2],
    [".5", 2],
    ["--1", 2],
  ])("rejects %j with exponent %i", (input, exponent) => {
    expect(parseAmount(input, exponent)).toBeNull();
  });
});
