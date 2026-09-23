import { renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { useIdempotencyKey } from "~/lib/idempotency";

describe("useIdempotencyKey", () => {
  it("keeps the key until an attempt finishes", () => {
    const { result, rerender } = renderHook(({ outcome }) => useIdempotencyKey(outcome), { initialProps: { outcome: undefined as unknown } });
    const first = result.current;

    rerender({ outcome: undefined });
    expect(result.current).toBe(first);

    rerender({ outcome: { error: "Debits and credits must be equal in each currency." } });
    const second = result.current;
    expect(second).not.toBe(first);

    rerender({ outcome: { error: null, created: "POINTS" } });
    expect(result.current).not.toBe(second);
  });
});
