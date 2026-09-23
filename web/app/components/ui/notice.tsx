import type { ReactNode } from "react";

type Tone = "neutral" | "warning" | "danger";

const tones: Record<Tone, string> = {
  neutral: "border-line text-ink",
  warning: "border-warning text-warning",
  danger: "border-danger text-danger",
};

/** A short message about the state of the page: empty, failed or needing attention. */
export function Notice({ tone = "neutral", children }: { tone?: Tone; children: ReactNode }) {
  return (
    <div role={tone === "danger" ? "alert" : "status"} className={`border px-3 py-2 ${tones[tone]}`}>
      {children}
    </div>
  );
}
