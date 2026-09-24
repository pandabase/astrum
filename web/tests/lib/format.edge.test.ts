import { describe, expect, it } from "vitest";
import { formatAmount, parseAmount } from "~/lib/format";

const max38 = "9".repeat(38);

describe("formatAmount edge cases", () => {
  it.each([
    ["000", 2, "0.00"],
    ["-000", 2, "0.00"],
    ["-0", 0, "0"],
    ["0", 0, "0"],
    ["1", 30, "0.000000000000000000000000000001"],
    ["-1", 30, "-0.000000000000000000000000000001"],
    ["1000000", 0, "1,000,000"],
    ["-1234567", 0, "-1,234,567"],
    ["999", 0, "999"],
    ["1000", 3, "1.000"],
    ["123", 3, "0.123"],
    ["00100", 2, "1.00"],
    [max38, 0, "99,999,999,999,999,999,999,999,999,999,999,999,999"],
    [max38, 30, "99,999,999.999999999999999999999999999999"],
    ["-" + max38, 2, "-999,999,999,999,999,999,999,999,999,999,999,999.99"],
    ["1" + "0".repeat(38), 2, "1,000,000,000,000,000,000,000,000,000,000,000,000.00"],
  ])("formats %s with exponent %i as %s", (minor, exponent, want) => {
    expect(formatAmount(minor, exponent)).toBe(want);
  });

  it.each([" 5", "5 ", "+5", "1,000", "0x10", "٣", "１２", "-", "5-", "-+5", "1_000"])("rejects %j", (minor) => {
    expect(() => formatAmount(minor, 2)).toThrow(/invalid amount/);
  });

  it("does not depend on the runtime locale", () => {
    expect(formatAmount("123456789", 2)).toBe("1,234,567.89");
    expect(formatAmount("123456789", 2)).not.toBe(Number(1234567.89).toLocaleString("de-DE"));
  });

  it.each([0, 2, 8, 18, 30])("round-trips through parseAmount with exponent %i", (exponent) => {
    for (const minor of ["1", "10", "123456789", max38, "-42"]) {
      expect(parseAmount(formatAmount(minor, exponent), exponent)).toBe(minor);
    }
  });
});

describe("parseAmount edge cases", () => {
  it.each([
    ["007", 2, "700"],
    ["007.50", 2, "750"],
    ["0000", 2, "0"],
    ["-0.00", 2, "0"],
    ["-000", 0, "0"],
    ["0.00", 2, "0"],
    ["0.01", 2, "1"],
    ["-0.01", 2, "-1"],
    ["5.", 0, "5"],
    ["5", 0, "5"],
    ["1,000,000", 0, "1000000"],
    ["1,2,3", 0, "123"],
    ["1,.5", 2, "150"],
    ["1.5,0", 2, "150"],
    ["\t12.5\n", 2, "1250"],
    [" 12 ", 0, "12"],
    ["1", 30, "1" + "0".repeat(30)],
    ["0." + "0".repeat(29) + "1", 30, "1"],
    [max38, 0, max38],
    ["999,999,999,999,999,999,999,999,999,999,999,999.99", 2, max38],
  ])("parses %j with exponent %i as %s", (input, exponent, want) => {
    expect(parseAmount(input, exponent)).toBe(want);
  });

  it.each([
    ["+5", 2],
    [".5", 2],
    ["-.5", 2],
    [".", 2],
    ["-", 2],
    [",", 2],
    ["1 000", 0],
    ["1.2.3", 2],
    ["5.0", 0],
    ["1.500", 2],
    ["1e2", 2],
    ["Infinity", 2],
    ["NaN", 2],
    ["0x10", 2],
    ["１２", 2],
    ["٣", 2],
    ["- 5", 2],
    ["5-", 2],
    ["$5", 2],
    ["0." + "0".repeat(30) + "1", 30],
  ])("rejects %j with exponent %i", (input, exponent) => {
    expect(parseAmount(input, exponent)).toBeNull();
  });

  it("treats a European decimal comma as a thousands separator", () => {
    expect(parseAmount("1.234,56", 2)).toBeNull();
    expect(parseAmount("1.234,56", 5)).toBe("123456");
  });

  it("does not cap the digit count, leaving the 38-digit limit to the API", () => {
    expect(parseAmount("9".repeat(39), 0)).toBe("9".repeat(39));
    expect(parseAmount(max38, 2)).toBe(max38 + "00");
  });

  it("produces strings BigInt accepts for totals", () => {
    const a = parseAmount(max38, 0)!;
    const b = parseAmount("1", 0)!;
    expect((BigInt(a) + BigInt(b)).toString()).toBe("1" + "0".repeat(38));
  });
});
