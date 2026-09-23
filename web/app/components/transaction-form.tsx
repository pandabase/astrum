import { useMemo, useState } from "react";
import { Form, useNavigation } from "react-router";
import { formatAmount } from "~/lib/format";
import { formatMetadata } from "~/lib/metadata";
import { toLocalInput } from "~/lib/time";
import { blankDraft, totals, type EntryDraft, type LockOperator, type LockView } from "~/lib/transaction-form";
import type { Account, Metadata, Side } from "~/lib/types";
import { Button } from "./ui/button";
import { Checkbox, Field, Input, Select, Textarea } from "./ui/field";
import { ButtonLink } from "./ui/link";
import { Notice } from "./ui/notice";

type Mode = "create" | "edit" | "schedule";

type TransactionFormProps = {
  mode: Mode;
  accounts: Account[];
  error?: string;
  submitLabel: string;
  cancelTo: string;
  idempotencyKey?: string;
  defaults?: {
    entries?: EntryDraft[];
    description?: string;
    metadata?: Metadata;
    effectiveAt?: string | null;
  };
};

const operators: { value: LockOperator; label: string }[] = [
  { value: "gte", label: "≥" },
  { value: "gt", label: ">" },
  { value: "eq", label: "=" },
  { value: "not_eq", label: "≠" },
  { value: "lte", label: "≤" },
  { value: "lt", label: "<" },
];

/**
 * Creates, edits or schedules a transaction. The entry rows are kept in state and sent as one hidden JSON field;
 * the action converts amounts using the accounts' exponents.
 */
export function TransactionForm({ mode, accounts, error, submitLabel, cancelTo, idempotencyKey, defaults }: TransactionFormProps) {
  const [drafts, setDrafts] = useState<EntryDraft[]>(() => defaults?.entries ?? [blankDraft("debit"), blankDraft("credit")]);
  const byId = useMemo(() => new Map(accounts.map((account) => [account.id, account])), [accounts]);
  const sums = totals(drafts, byId);
  const unbalanced = sums.filter((total) => total.debits !== total.credits);
  const submitting = useNavigation().state === "submitting";

  const update = (key: string, change: Partial<EntryDraft>) =>
    setDrafts((rows) => rows.map((row) => (row.key === key ? { ...row, ...change } : row)));

  return (
    <Form method="post" className="grid max-w-4xl gap-5">
      {idempotencyKey && <input type="hidden" name="idempotency_key" value={idempotencyKey} />}
      <input type="hidden" name="entries" value={JSON.stringify(drafts)} />
      {mode === "edit" && <input type="hidden" name="original_metadata" value={JSON.stringify(defaults?.metadata ?? {})} />}
      {error && <Notice tone="danger">{error}</Notice>}

      {mode === "schedule" && (
        <Field label="Post at" hint="The transaction posts automatically at this time, in your time zone.">
          {(props) => <Input {...props} name="execute_at" type="datetime-local" required className="w-64" />}
        </Field>
      )}
      {mode !== "edit" && (
        <fieldset className="grid gap-2">
          <legend className="mb-1 font-medium">Status</legend>
          <label className="flex items-start gap-2">
            <input type="radio" name="status" value="posted" defaultChecked className="mt-0.5 accent-ink" />
            <span>
              Posted <span className="text-muted">— recorded in the journal at once.</span>
            </span>
          </label>
          <label className="flex items-start gap-2">
            <input type="radio" name="status" value="pending" className="mt-0.5 accent-ink" />
            <span>
              Pending <span className="text-muted">— reserves outflows now; post or archive it later.</span>
            </span>
          </label>
        </fieldset>
      )}

      <fieldset className="grid gap-2">
        <legend className="mb-1 font-medium">Entries</legend>
        {accounts.length === 0 && <Notice tone="warning">This ledger has no accounts yet.</Notice>}
        <div className="grid gap-2">
          {drafts.map((draft, i) => (
            <EntryRow
              key={draft.key}
              index={i}
              draft={draft}
              accounts={accounts}
              account={byId.get(draft.accountId)}
              onChange={(change) => update(draft.key, change)}
              onRemove={drafts.length > 2 ? () => setDrafts((rows) => rows.filter((row) => row.key !== draft.key)) : undefined}
            />
          ))}
        </div>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <Button onClick={() => setDrafts((rows) => [...rows, blankDraft(rows.length % 2 === 0 ? "debit" : "credit")])}>Add entry</Button>
          <div aria-live="polite" className="grid justify-items-end tabular-nums">
            {sums.map((total) => (
              <p key={total.currency} className={total.debits === total.credits ? "text-muted" : "text-danger"}>
                {total.currency}: debits {formatAmount(String(total.debits), total.exponent)}, credits{" "}
                {formatAmount(String(total.credits), total.exponent)}
                {total.debits === total.credits
                  ? " — balanced"
                  : ` — off by ${formatAmount(String(total.debits > total.credits ? total.debits - total.credits : total.credits - total.debits), total.exponent)}`}
              </p>
            ))}
          </div>
        </div>
      </fieldset>

      <Field label="Description">
        {(props) => <Input {...props} name="description" defaultValue={defaults?.description} maxLength={1000} />}
      </Field>
      {mode !== "edit" && (
        <Field label="External ID" hint="Your own reference, unique within the ledger. Optional.">
          {(props) => <Input {...props} name="external_id" maxLength={255} className="w-80 font-mono" />}
        </Field>
      )}
      <Field label="Effective date" hint="When the transaction counts for accounting, in your time zone. Leave blank for now.">
        {(props) => <Input {...props} name="effective_at" type="datetime-local" defaultValue={toLocalInput(defaults?.effectiveAt)} className="w-64" />}
      </Field>
      <Field label="Metadata" hint='A JSON object, such as {"invoice": "inv-1"}. Leave blank for none.'>
        {(props) => (
          <Textarea {...props} name="metadata" defaultValue={formatMetadata(defaults?.metadata)} spellCheck={false} className="min-h-16 font-mono text-xs" />
        )}
      </Field>
      {mode !== "edit" && (
        <Checkbox
          name="archive_on_balance_lock_failure"
          label="Record as archived if a balance condition fails"
          hint="Instead of rejecting the transaction, keep it as archived with no effect on balances."
        />
      )}

      <div className="flex items-center gap-2">
        <Button type="submit" variant="primary" disabled={submitting}>
          {submitting ? "Saving…" : submitLabel}
        </Button>
        <ButtonLink to={cancelTo}>Cancel</ButtonLink>
        {unbalanced.length > 0 && <span className="text-danger">Debits and credits must be equal in each currency.</span>}
      </div>
    </Form>
  );
}

type EntryRowProps = {
  index: number;
  draft: EntryDraft;
  accounts: Account[];
  account?: Account;
  onChange: (change: Partial<EntryDraft>) => void;
  onRemove?: () => void;
};

function EntryRow({ index, draft, accounts, account, onChange, onRemove }: EntryRowProps) {
  const label = `Entry ${index + 1}`;
  return (
    <div className="grid gap-2 border border-line p-2">
      <div className="grid grid-cols-[minmax(0,1fr)_7rem_10rem_auto] items-center gap-2">
        <Select aria-label={`${label} account`} value={draft.accountId} onChange={(e) => onChange({ accountId: e.target.value })} required>
          <option value="">Choose an account</option>
          {accounts.map((a) => (
            <option key={a.id} value={a.id} disabled={a.status !== "open"}>
              {a.code}
              {a.name ? ` — ${a.name}` : ""} ({a.currency}, {a.normal_side}){a.status !== "open" ? ` [${a.status}]` : ""}
            </option>
          ))}
        </Select>
        <Select aria-label={`${label} side`} value={draft.side} onChange={(e) => onChange({ side: e.target.value as Side })}>
          <option value="debit">Debit</option>
          <option value="credit">Credit</option>
        </Select>
        <Input
          aria-label={`${label} amount`}
          value={draft.amount}
          onChange={(e) => onChange({ amount: e.target.value })}
          inputMode="decimal"
          placeholder={account ? `0.${"0".repeat(account.currency_exponent)}`.replace(/\.$/, "") : "Amount"}
          className="text-right tabular-nums"
          required
        />
        {onRemove ? (
          <Button aria-label={`Remove ${label.toLowerCase()}`} onClick={onRemove}>
            Remove
          </Button>
        ) : (
          <span className="w-[4.5rem]" />
        )}
      </div>
      <details open={draft.lockView !== "" || draft.requireVersion}>
        <summary className="cursor-pointer text-muted">Conditions</summary>
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <span className="text-muted">After this transaction, the</span>
          <Select aria-label={`${label} condition balance`} value={draft.lockView} onChange={(e) => onChange({ lockView: e.target.value as LockView })} className="w-36">
            <option value="">no condition</option>
            <option value="available">available</option>
            <option value="pending">pending</option>
            <option value="posted">posted</option>
          </Select>
          {draft.lockView && (
            <>
              <span className="text-muted">balance must be</span>
              <Select aria-label={`${label} condition operator`} value={draft.lockOperator} onChange={(e) => onChange({ lockOperator: e.target.value as LockOperator })} className="w-16">
                {operators.map((op) => (
                  <option key={op.value} value={op.value}>
                    {op.label}
                  </option>
                ))}
              </Select>
              <Input
                aria-label={`${label} condition amount`}
                value={draft.lockAmount}
                onChange={(e) => onChange({ lockAmount: e.target.value })}
                inputMode="decimal"
                className="w-32 text-right tabular-nums"
              />
            </>
          )}
        </div>
        <div className="mt-2">
          <Checkbox
            label="Fail if the account changed since this page loaded"
            hint={account ? `Uses lock version ${account.lock_version}.` : undefined}
            checked={draft.requireVersion}
            onChange={(e) => onChange({ requireVersion: e.target.checked })}
          />
        </div>
      </details>
    </div>
  );
}
