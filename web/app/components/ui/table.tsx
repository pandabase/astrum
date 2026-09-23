import type { ComponentProps } from "react";
import { cn } from "~/lib/cn";

export function Table({ className, ...props }: ComponentProps<"table">) {
  return (
    <div role="region" aria-label="Scrollable data table" tabIndex={0} className="max-h-[70dvh] overflow-auto overscroll-x-contain border border-line focus-visible:outline-offset-2">
      <table className={cn("w-full border-collapse [&_tbody_tr]:transition-colors [&_tbody_tr:hover]:bg-sunken [&_tbody_tr:focus-within]:bg-sunken motion-reduce:[&_tbody_tr]:transition-none [&_tbody_tr:last-child_td]:border-b-0", className)} {...props} />
    </div>
  );
}

type CellProps = { numeric?: boolean };

export function Th({ numeric, className, ...props }: ComponentProps<"th"> & CellProps) {
  return (
    <th
      scope="col"
      className={cn("sticky top-0 z-10 border-b border-line bg-sunken px-4 py-3 text-xs font-medium text-muted", numeric ? "text-right" : "text-left", className)}
      {...props}
    />
  );
}

export function Td({ numeric, className, ...props }: ComponentProps<"td"> & CellProps) {
  return <td className={cn("border-b border-line px-4 py-3", numeric && "text-right tabular-nums", className)} {...props} />;
}
