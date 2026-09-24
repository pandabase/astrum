import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { toApiTime, toLocalInput } from "~/lib/time";

const originalTz = process.env.TZ;

function inZone(zone: string, run: () => void) {
  describe(`in ${zone}`, () => {
    beforeAll(() => {
      process.env.TZ = zone;
    });
    afterAll(() => {
      process.env.TZ = originalTz;
    });
    run();
  });
}

describe("toApiTime edge cases", () => {
  it.each(["   ", "\t", "2026-13-01T00:00", "2026-02-30Tab", "T14:30", "24:00"])("is null for %j", (local) => {
    expect(toApiTime(local)).toBeNull();
  });

  it("honors an explicit offset instead of the browser's zone", () => {
    expect(toApiTime("2026-09-24T14:30Z")).toBe("2026-09-24T14:30:00.000Z");
    expect(toApiTime("2026-09-24T14:30:00+05:30")).toBe("2026-09-24T09:00:00.000Z");
  });

  it("returns full precision UTC with milliseconds", () => {
    expect(toApiTime("2026-09-24T14:30:45.123Z")).toBe("2026-09-24T14:30:45.123Z");
  });
});

describe("toLocalInput edge cases", () => {
  it("is blank for undefined and empty", () => {
    expect(toLocalInput(undefined)).toBe("");
    expect(toLocalInput("")).toBe("");
  });
});

inZone("UTC", () => {
  it("keeps seconds and milliseconds in the API time but drops them from the input", () => {
    expect(toApiTime("2026-09-24T14:30:45")).toBe("2026-09-24T14:30:45.000Z");
    expect(toApiTime("2026-09-24T14:30:45.678")).toBe("2026-09-24T14:30:45.678Z");
    expect(toLocalInput("2026-09-24T14:30:59.999Z")).toBe("2026-09-24T14:30");
  });

  it("pads single-digit months, days, hours and minutes", () => {
    expect(toLocalInput("2026-01-02T03:04:05Z")).toBe("2026-01-02T03:04");
  });

  it("converts offsets to the local zone", () => {
    expect(toLocalInput("2026-09-24T23:30:00-02:00")).toBe("2026-09-25T01:30");
  });

  it("handles leap days and year ends", () => {
    expect(toApiTime("2028-02-29T12:00")).toBe("2028-02-29T12:00:00.000Z");
    expect(toLocalInput("2026-12-31T23:59:59Z")).toBe("2026-12-31T23:59");
  });

  it("pads years before 1000 to the four digits datetime-local requires", () => {
    expect(toLocalInput("0999-06-01T00:00:00Z")).toBe("0999-06-01T00:00");
  });
});

inZone("America/New_York", () => {
  it("reads the input in the browser's zone", () => {
    expect(toApiTime("2026-09-24T14:30")).toBe("2026-09-24T18:30:00.000Z");
    expect(toApiTime("2026-01-15T14:30")).toBe("2026-01-15T19:30:00.000Z");
    expect(toLocalInput("2026-09-24T18:30:00Z")).toBe("2026-09-24T14:30");
  });

  it("moves a time skipped by the spring-forward gap an hour later", () => {
    const iso = toApiTime("2026-03-08T02:30");
    expect(iso).toBe("2026-03-08T07:30:00.000Z");
    expect(toLocalInput(iso)).toBe("2026-03-08T03:30");
  });

  it("picks the first of the two times repeated at fall back", () => {
    expect(toApiTime("2026-11-01T01:30")).toBe("2026-11-01T05:30:00.000Z");
    expect(toLocalInput("2026-11-01T05:30:00Z")).toBe("2026-11-01T01:30");
    expect(toLocalInput("2026-11-01T06:30:00Z")).toBe("2026-11-01T01:30");
  });

  it("round-trips ordinary times across both DST edges", () => {
    for (const local of ["2026-03-08T01:59", "2026-03-08T03:00", "2026-11-01T00:59", "2026-11-01T02:00"]) {
      expect(toLocalInput(toApiTime(local))).toBe(local);
    }
  });
});

inZone("Asia/Kolkata", () => {
  it("handles half-hour offsets and date changes", () => {
    expect(toApiTime("2026-09-25T02:00")).toBe("2026-09-24T20:30:00.000Z");
    expect(toLocalInput("2026-09-24T20:30:00Z")).toBe("2026-09-25T02:00");
  });
});
