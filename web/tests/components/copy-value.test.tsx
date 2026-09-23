import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CopyValue } from "~/components/copy-value";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("copy value", () => {
  it("copies the complete ID and announces success", async () => {
    const write = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    render(<CopyValue value="txn_example" />);
    fireEvent.click(screen.getByRole("button", { name: "Copy txn_example" }));
    await screen.findByText("Copied");
    expect(write).toHaveBeenCalledWith("txn_example");
  });

  it("keeps the value available when clipboard access fails", async () => {
    vi.spyOn(navigator.clipboard, "writeText").mockRejectedValue(new Error("Blocked"));
    render(<CopyValue value="txn_example" />);
    fireEvent.click(screen.getByRole("button", { name: "Copy txn_example" }));
    await screen.findByText("Could not copy. Select the text to copy it.");
    expect(screen.getByText("txn_example")).toBeTruthy();
  });
});
