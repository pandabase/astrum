import { useState } from "react";
import { Button } from "./ui/button";

/** A secret shown once, with a copy button, because the API never returns it again. */
export function OneTimeSecret({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div role="status" className="grid gap-2 border border-warning p-3">
      <p className="font-medium">{label}</p>
      <p className="text-muted">Copy it now. It is not shown again.</p>
      <div className="flex items-center gap-2">
        <code className="min-w-0 flex-1 overflow-x-auto border border-line bg-sunken px-2 py-1.5 font-mono text-xs">{value}</code>
        <Button
          onClick={async () => {
            await navigator.clipboard.writeText(value);
            setCopied(true);
          }}
        >
          {copied ? "Copied" : "Copy"}
        </Button>
      </div>
    </div>
  );
}
