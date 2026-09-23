import { useState } from "react";

export function CopyValue({ value }: { value: string }) {
  const [message, setMessage] = useState("");

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setMessage("Copied");
    } catch {
      setMessage("Could not copy. Select the text to copy it.");
    }
  }

  return (
    <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
      <span className="font-mono text-xs select-all">{value}</span>
      <button type="button" onClick={copy} onBlur={() => setMessage("")} aria-label={`Copy ${value}`} className="min-h-8 px-1 text-xs text-muted underline-offset-4 hover:text-ink hover:underline">Copy</button>
      <span role="status" className="text-xs text-muted">{message}</span>
    </span>
  );
}
