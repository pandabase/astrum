import { cn } from "~/lib/cn";

// Color only marks states that need attention: waiting, failed, or no longer usable.
const tones: Record<string, string> = {
  pending: "text-warning",
  scheduled: "text-warning",
  processing: "text-warning",
  frozen: "text-warning",
  failed: "text-danger",
  archived: "text-muted",
  voided: "text-muted",
  expired: "text-muted",
  canceled: "text-muted",
  closed: "text-muted",
  disabled: "text-muted",
};

export function StatusText({ status, className }: { status: string; className?: string }) {
  return <span className={cn(tones[status], className)}>{status}</span>;
}
