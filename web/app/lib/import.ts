type Parsed = { ok: true; transactions: unknown[] } | { ok: false; error: string };

/**
 * Reads the transactions to import: either {"transactions": [...]} as the API takes it, or a bare array. Each item
 * is sent as is; the API validates it.
 */
export function parseImport(text: string, max: number): Parsed {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    return { ok: false, error: "The file is not valid JSON." };
  }
  const transactions = Array.isArray(value)
    ? value
    : typeof value === "object" && value !== null && Array.isArray((value as { transactions?: unknown }).transactions)
      ? (value as { transactions: unknown[] }).transactions
      : null;
  if (!transactions) return { ok: false, error: 'Expected {"transactions": [...]} or an array of transactions.' };
  if (transactions.length === 0) return { ok: false, error: "There are no transactions to import." };
  if (transactions.length > max) return { ok: false, error: `This mode takes at most ${max.toLocaleString()} transactions; the file has ${transactions.length.toLocaleString()}.` };
  if (!transactions.every((t) => typeof t === "object" && t !== null && !Array.isArray(t))) {
    return { ok: false, error: "Every transaction must be a JSON object." };
  }
  return { ok: true, transactions };
}
