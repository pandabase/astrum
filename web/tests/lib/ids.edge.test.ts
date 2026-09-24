import { describe, expect, it, vi } from "vitest";
import { isId, routeId, type IdPrefix } from "~/lib/ids";

const suffix = "01h455vb4pex5vsknk084sn02q";

describe("isId edge cases", () => {
  it.each([
    ["ldg_" + suffix, true],
    ["ldg_0" + "0".repeat(25), true],
    ["ldg_7" + "z".repeat(25), true],
    ["ldg_" + suffix.slice(0, 25), false],
    ["ldg_" + suffix + "0", false],
    ["ldg_8" + suffix.slice(1), false],
    ["ldg_9" + suffix.slice(1), false],
    ["ldg_a" + suffix.slice(1), false],
    ["LDG_" + suffix, false],
    ["Ldg_" + suffix, false],
    ["ldg_" + suffix.toUpperCase(), false],
    ["ldg-" + suffix, false],
    ["ldg" + suffix, false],
    ["ldg__" + suffix.slice(1), false],
    ["_" + suffix, false],
    [suffix, false],
    ["xldg_" + suffix, false],
    ["ldg_" + suffix + "\n", false],
    ["\nldg_" + suffix, false],
    [" ldg_" + suffix, false],
    ["ldg_" + suffix + " ", false],
    ["ldg_" + suffix.slice(0, 24) + "/q", false],
    ["ldg_" + suffix.slice(0, 23) + "%2F", false],
    ["ldg_" + suffix.slice(0, 24) + "..", false],
    ["..", false],
    ["ldg_" + suffix.slice(0, 25) + "ǫ", false],
    ["", false],
  ])("%j as ldg is %s", (value, want) => {
    expect(isId(value, "ldg")).toBe(want);
  });

  it.each(["i", "l", "o", "u"])("rejects the excluded letter %s", (letter) => {
    expect(isId("ldg_" + suffix.slice(0, 25) + letter, "ldg")).toBe(false);
  });

  it.each(["a", "h", "j", "k", "m", "n", "p", "t", "v", "z"])("accepts the base32 letter %s", (letter) => {
    expect(isId("ldg_" + suffix.slice(0, 25) + letter, "ldg")).toBe(true);
  });

  it("accepts every prefix only for its own kind", () => {
    const prefixes: IdPrefix[] = ["ldg", "acct", "txn", "hold", "sched", "stmt", "cat", "bm", "blk", "stl", "evt", "we", "wd", "key"];
    for (const prefix of prefixes) {
      for (const other of prefixes) {
        expect(isId(`${prefix}_${suffix}`, other)).toBe(prefix === other);
      }
    }
  });
});

describe("routeId edge cases", () => {
  it.each([undefined, "", "..", "%2e%2e", "ldg_" + suffix + "/edit", "acct_" + suffix])("throws a 404 for %j without calling the API", (value) => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    try {
      routeId(value, "ldg");
      expect.unreachable();
    } catch (err) {
      expect(err).toMatchObject({ init: { status: 404 } });
    } finally {
      vi.unstubAllGlobals();
    }
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
