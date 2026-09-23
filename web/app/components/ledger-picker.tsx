import { Form } from "react-router";
import type { Ledger } from "~/lib/types";
import { Button } from "./ui/button";
import { Field, Select } from "./ui/field";
import { Notice } from "./ui/notice";

/** Asks which ledger to work in, for pages whose accounts must all come from one ledger. */
export function LedgerPicker({ ledgers, action = "Continue" }: { ledgers: Ledger[]; action?: string }) {
  if (ledgers.length === 0) return <Notice>Create a ledger first.</Notice>;
  return (
    <Form className="flex max-w-xl items-end gap-2">
      <div className="flex-1">
        <Field label="Ledger">
          {(props) => (
            <Select {...props} name="ledger_id" required>
              {ledgers.map((ledger) => (
                <option key={ledger.id} value={ledger.id}>
                  {ledger.name}
                </option>
              ))}
            </Select>
          )}
        </Field>
      </div>
      <Button type="submit" variant="primary">
        {action}
      </Button>
    </Form>
  );
}
