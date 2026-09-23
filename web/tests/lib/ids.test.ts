import { describe, expect, it } from "vitest";
import { isId, routeId } from "~/lib/ids";

describe("isId", () => {
  it.each([
    ["ldg_01h455vb4pex5vsknk084sn02q", "ldg", true],
    ["acct_7zzzzzzzzzzzzzzzzzzzzzzzzz", "acct", true],
    ["acct_01h455vb4pex5vsknk084sn02q", "ldg", false],
    ["ldg_81h455vb4pex5vsknk084sn02q", "ldg", false],
    ["ldg_01h455vb4pex5vsknk084sn02", "ldg", false],
    ["ldg_01h455vb4pex5vsknk084sn02qq", "ldg", false],
    ["ldg_01H455VB4PEX5VSKNK084SN02Q", "ldg", false],
    ["ldg_01h455vb4pex5vsknk084sn0iq", "ldg", false],
    ["x/../api_keys", "ldg", false],
    [undefined, "ldg", false],
  ] as const)("%s as %s is %s", (value, prefix, want) => {
    expect(isId(value, prefix)).toBe(want);
  });
});

describe("routeId", () => {
  it("returns a valid id", () => {
    expect(routeId("ldg_01h455vb4pex5vsknk084sn02q", "ldg")).toBe("ldg_01h455vb4pex5vsknk084sn02q");
  });

  it("throws a 404 for anything else", () => {
    try {
      routeId("x/../api_keys", "ldg");
      expect.unreachable();
    } catch (err) {
      expect(err).toMatchObject({ init: { status: 404 } });
    }
  });
});
