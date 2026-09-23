type NormalSideFieldProps = { debitHint: string; creditHint: string };

/** Picks debit or credit as the side that makes a balance grow. */
export function NormalSideField({ debitHint, creditHint }: NormalSideFieldProps) {
  return (
    <fieldset className="grid gap-2">
      <legend className="mb-1 font-medium">Normal side</legend>
      {(
        [
          ["debit", "Debit", debitHint],
          ["credit", "Credit", creditHint],
        ] as const
      ).map(([value, label, hint], i) => (
        <label key={value} className="flex items-start gap-2">
          <input type="radio" name="normal_side" value={value} required={i === 0} className="mt-0.5 accent-ink" />
          <span>
            {label} <span className="text-muted">— {hint}</span>
          </span>
        </label>
      ))}
    </fieldset>
  );
}
