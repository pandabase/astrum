import { describe, expect, it } from "vitest";
import { cn } from "~/lib/cn";

describe("cn", () => {
  it("lets later classes override earlier ones, including theme colors", () => {
    expect(cn("h-8 w-full border", "w-40")).toBe("h-8 border w-40");
    expect(cn("text-ink bg-canvas", "text-danger")).toBe("bg-canvas text-danger");
    expect(cn("border-line-strong", "border-danger")).toBe("border-danger");
  });

  it("drops falsy values", () => {
    expect(cn("px-3", false, undefined, "py-2")).toBe("px-3 py-2");
  });
});
