import { formatAmount } from "~/lib/format";

type AmountProps = {
  /** Integer minor units as the API returns them. */
  value: string;
  exponent: number;
  currency?: string;
};

export function Amount({ value, exponent, currency }: AmountProps) {
  return (
    <span className="tabular-nums whitespace-nowrap">
      {formatAmount(value, exponent)}
      {currency && <span className="text-muted"> {currency}</span>}
    </span>
  );
}
