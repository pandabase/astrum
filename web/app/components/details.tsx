import type { ReactNode } from "react";
import { cn } from "~/lib/cn";

export type Detail = { term: string; value: ReactNode; mono?: boolean };

/** Label and value pairs describing one resource. */
export function Details({ items }: { items: Detail[] }) {
  return (
    <dl className="grid grid-cols-[10rem_1fr]">
      {items.map(({ term, value, mono }) => (
        <div key={term} className="contents">
          <dt className="border-b border-line py-2 text-muted">{term}</dt>
          <dd className={cn("min-w-0 border-b border-line py-2 break-words", mono && "font-mono text-xs leading-5")}>
            {value}
          </dd>
        </div>
      ))}
    </dl>
  );
}

/** Shows metadata as formatted JSON, or a dash when there is none. */
export function MetadataValue({ metadata }: { metadata: Record<string, unknown> | null | undefined }) {
  if (!metadata || Object.keys(metadata).length === 0) return <span className="text-muted">None</span>;
  return <pre className="font-mono text-xs leading-5 whitespace-pre-wrap">{JSON.stringify(metadata, null, 2)}</pre>;
}
