import { useState, type ReactNode } from "react";
import { useFetcher } from "react-router";
import { Button } from "./ui/button";
import { Dialog } from "./ui/dialog";
import { Notice } from "./ui/notice";

type ConfirmActionProps = {
  /** Shared with ActionError so the result shows on the page after the dialog closes. */
  fetcherKey: string;
  label: string;
  title: string;
  confirmLabel: string;
  intent: string;
  variant?: "secondary" | "danger";
  /** Explanation and optional extra fields, submitted with the intent. */
  children: ReactNode;
};

/** A button that asks before submitting an intent to the page's action. */
export function ConfirmAction({ fetcherKey, label, title, confirmLabel, intent, variant = "secondary", children }: ConfirmActionProps) {
  const fetcher = useFetcher({ key: fetcherKey });
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button variant={variant} onClick={() => setOpen(true)} disabled={fetcher.state !== "idle"}>
        {label}
      </Button>
      <Dialog open={open} onClose={() => setOpen(false)} title={title}>
        <fetcher.Form method="post" className="grid gap-4" onSubmit={() => setOpen(false)}>
          <input type="hidden" name="intent" value={intent} />
          {children}
          <div className="flex gap-2">
            <Button type="submit" variant={variant === "danger" ? "danger" : "primary"}>
              {confirmLabel}
            </Button>
            <Button onClick={() => setOpen(false)}>Cancel</Button>
          </div>
        </fetcher.Form>
      </Dialog>
    </>
  );
}

/** A button that submits an intent to the page's action without asking. */
export function ActionButton({ fetcherKey, intent, children, variant }: { fetcherKey: string; intent: string; children: ReactNode; variant?: "primary" | "secondary" | "danger" }) {
  const fetcher = useFetcher({ key: fetcherKey });
  return (
    <fetcher.Form method="post" className="contents">
      <Button type="submit" name="intent" value={intent} variant={variant} disabled={fetcher.state !== "idle"}>
        {children}
      </Button>
    </fetcher.Form>
  );
}

/** Shows the error an action returned, if any. */
export function ActionError({ fetcherKey }: { fetcherKey: string }) {
  const data = useFetcher<{ error?: string } | null>({ key: fetcherKey }).data;
  return data?.error ? <Notice tone="danger">{data.error}</Notice> : null;
}
