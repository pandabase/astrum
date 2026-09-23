import { Input } from "./ui/field";

/** The effective_at range filters: from is included, until is not. */
export function DateRangeFields({ from, until }: { from: string; until: string }) {
  return (
    <>
      <label className="grid gap-1">
        <span className="text-xs text-muted">Effective from</span>
        <Input name="from" type="datetime-local" defaultValue={from} className="w-52" />
      </label>
      <label className="grid gap-1">
        <span className="text-xs text-muted">Effective before</span>
        <Input name="until" type="datetime-local" defaultValue={until} className="w-52" />
      </label>
    </>
  );
}
