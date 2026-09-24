import { renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { useIdempotencyKey } from "~/lib/idempotency";

function setup(initial: unknown) {
  return renderHook(({ outcome }) => useIdempotencyKey(outcome), { initialProps: { outcome: initial } });
}

describe("useIdempotencyKey edge cases", () => {
  it("is a UUID", () => {
    expect(setup(undefined).result.current).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  });

  it("differs between two forms", () => {
    expect(setup(undefined).result.current).not.toBe(setup(undefined).result.current);
  });

  it("keeps the key across many re-renders without a result", () => {
    const { result, rerender } = setup(undefined);
    const first = result.current;
    for (let i = 0; i < 5; i++) rerender({ outcome: undefined });
    expect(result.current).toBe(first);
  });

  it("does not rotate on null", () => {
    const { result, rerender } = setup(undefined);
    const first = result.current;
    rerender({ outcome: null });
    expect(result.current).toBe(first);
    rerender({ outcome: undefined });
    expect(result.current).toBe(first);
  });

  it("does not rotate for a result it started with", () => {
    const outcome = { error: "x" };
    const { result, rerender } = setup(outcome);
    const first = result.current;
    rerender({ outcome });
    expect(result.current).toBe(first);
  });

  it("rotates after a success", () => {
    const { result, rerender } = setup(undefined);
    const first = result.current;
    rerender({ outcome: { error: null, batch: {} } });
    expect(result.current).not.toBe(first);
  });

  it("rotates for each new result object, even with the same error", () => {
    const { result, rerender } = setup(undefined);
    rerender({ outcome: { error: "same" } });
    const second = result.current;
    rerender({ outcome: { error: "same" } });
    expect(result.current).not.toBe(second);
  });

  it("keeps the key when the same result object is rendered again", () => {
    const outcome = { error: "same" };
    const { result, rerender } = setup(undefined);
    rerender({ outcome });
    const second = result.current;
    rerender({ outcome });
    expect(result.current).toBe(second);
  });

  it("keeps the rotated key when the result clears", () => {
    const { result, rerender } = setup(undefined);
    rerender({ outcome: { error: "x" } });
    const second = result.current;
    rerender({ outcome: undefined });
    expect(result.current).toBe(second);
  });

  it("rotates for non-object results", () => {
    const { result, rerender } = setup(undefined);
    const first = result.current;
    rerender({ outcome: 0 });
    const second = result.current;
    expect(second).not.toBe(first);
    rerender({ outcome: "" });
    expect(result.current).not.toBe(second);
  });
});
