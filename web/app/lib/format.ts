/**
 * Formats an integer amount of minor units as a decimal with thousands separators. Amounts reach 38 digits, so this
 * works on the string rather than converting to a number.
 */
export function formatAmount(minor: string, exponent: number): string {
  if (!/^-?\d+$/.test(minor)) {
    throw new Error(`invalid amount ${JSON.stringify(minor)}`);
  }
  const negative = minor.startsWith("-");
  const digits = (negative ? minor.slice(1) : minor).replace(/^0+(?=\d)/, "").padStart(exponent + 1, "0");
  const whole = digits.slice(0, digits.length - exponent);
  const fraction = digits.slice(digits.length - exponent);
  const grouped = whole.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  const sign = negative && /[1-9]/.test(digits) ? "-" : "";
  return sign + grouped + (exponent > 0 ? "." + fraction : "");
}

const dateTime = new Intl.DateTimeFormat(undefined, {
  year: "numeric",
  month: "short",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
});

export function formatDateTime(iso: string): string {
  return dateTime.format(new Date(iso));
}

/**
 * Converts a decimal typed by a person, such as "1,234.5", into integer minor units for a currency with the given
 * exponent. Returns null when the text is not a plain decimal or has more fraction digits than the currency allows.
 */
export function parseAmount(text: string, exponent: number): string | null {
  const match = /^(-?)(\d+)(?:\.(\d*))?$/.exec(text.trim().replaceAll(",", ""));
  if (!match) return null;
  const [, sign, whole, fraction = ""] = match;
  if (fraction.length > exponent) return null;
  const digits = (whole + fraction.padEnd(exponent, "0")).replace(/^0+(?=\d)/, "");
  return digits === "0" ? "0" : sign + digits;
}
