export type Role = "admin" | "write" | "read";

export type ApiKey = {
  object: "api_key";
  id: string;
  name: string;
  role: Role;
  hint: string;
  created_by: string | null;
  created_at: string;
  expires_at: string | null;
  revoked_at: string | null;
  last_used_at: string | null;
};

export type List<T> = {
  object: "list";
  data: T[];
  has_more: boolean;
  next_cursor: string | null;
};

/** An RFC 9457 problem as the API returns it. */
export type Problem = {
  type?: string;
  title?: string;
  status?: number;
  code?: string;
  detail?: string;
  request_id?: string;
};

export type Metadata = Record<string, unknown>;

export type Side = "debit" | "credit";

export type Ledger = {
  object: "ledger";
  id: string;
  name: string;
  description: string;
  metadata: Metadata;
  version: number;
  created_at: string;
};

export type Currency = {
  object: "currency";
  code: string;
  exponent: number;
  created_at: string;
};

/** One view of a balance; amount is the net on the account's normal side. */
export type Balance = {
  debits: string;
  credits: string;
  amount: string;
};

export type Balances = {
  pending: Balance;
  posted: Balance;
  available: Balance;
};

export type AccountStatus = "open" | "frozen" | "closed";

export type Account = {
  object: "account";
  id: string;
  ledger_id: string;
  code: string;
  name: string;
  description: string;
  metadata: Metadata;
  currency: string;
  currency_exponent: number;
  normal_side: Side;
  allow_negative: boolean;
  overdraft_limit: string;
  balances: Balances;
  held: string;
  status: AccountStatus;
  lock_version: number;
  status_changed_at: string | null;
  created_at: string;
};

export type AccountEntry = {
  object: "account_entry";
  transaction_id: string;
  side: Side;
  amount: string;
  currency: string;
  balance_after: string;
  created_at: string;
};

export type TransactionStatus = "pending" | "posted" | "archived";

export type BalanceCondition = Partial<Record<"gt" | "gte" | "eq" | "lt" | "lte" | "not_eq", string>>;

/** An entry as a transaction carries it. Balance locks are only sent; resulting balances only come back. */
export type EntryLine = {
  account_id: string;
  side: Side;
  amount: string;
  currency?: string;
  pending_balance_amount?: BalanceCondition;
  posted_balance_amount?: BalanceCondition;
  available_balance_amount?: BalanceCondition;
  lock_version?: number;
  resulting_balances?: Balances;
};

export type Transaction = {
  object: "transaction";
  id: string;
  ledger_id: string;
  idempotency_key: string;
  external_id: string | null;
  status: TransactionStatus;
  version: number;
  description: string;
  metadata: Metadata;
  reverses_id: string | null;
  entries: EntryLine[];
  effective_at: string;
  created_at: string;
  posted_at: string | null;
  archived_at: string | null;
};

/** One posting as the entries search returns it. */
export type Entry = {
  object: "entry";
  sequence: string;
  transaction_id: string;
  ledger_id: string;
  account_id: string;
  status: TransactionStatus;
  side: Side;
  amount: string;
  currency: string;
  balance_after: string | null;
  effective_at: string;
  created_at: string;
  settlement_id: string | null;
};

export type HoldStatus = "pending" | "captured" | "voided" | "expired";

export type Hold = {
  object: "hold";
  id: string;
  idempotency_key: string;
  account_id: string;
  amount: string;
  currency: string;
  status: HoldStatus;
  description: string;
  expires_at: string;
  captured_amount: string | null;
  capture_transaction_id: string | null;
  created_at: string;
  resolved_at: string | null;
};

export type ScheduleStatus = "scheduled" | "executed" | "failed" | "canceled";

export type ScheduledTransaction = {
  object: "scheduled_transaction";
  id: string;
  idempotency_key: string;
  execute_at: string;
  status: ScheduleStatus;
  description: string;
  metadata: Metadata;
  entries: EntryLine[];
  transaction_id: string | null;
  failure: string | null;
  created_at: string;
  resolved_at: string | null;
};

export type AccountBalances = Balances & {
  object: "balances";
  account_id: string;
  effective_at_lower_bound: string | null;
  effective_at_upper_bound: string | null;
};

export type Statement = {
  object: "statement";
  id: string;
  ledger_id: string;
  account_id: string;
  currency: string;
  description: string;
  effective_at_lower_bound: string;
  effective_at_upper_bound: string;
  starting_balance: Balance;
  ending_balance: Balance;
  entry_count: number;
  created_at: string;
};

export type Category = {
  object: "account_category";
  id: string;
  ledger_id: string;
  currency: string;
  normal_side: Side;
  name: string;
  description: string;
  metadata: Metadata;
  version: number;
  balances: Balances;
  created_at: string;
};

export type Settlement = {
  object: "settlement";
  id: string;
  idempotency_key: string;
  ledger_id: string;
  settled_account_id: string;
  contra_account_id: string;
  currency: string;
  effective_at_upper_bound: string | null;
  amount: string;
  entry_count: number;
  transaction_id: string | null;
  description: string;
  metadata: Metadata;
  created_at: string;
};

export type MonitorField = "available_balance_amount" | "pending_balance_amount" | "posted_balance_amount";
export type MonitorOperator = "gt" | "gte" | "eq" | "lt" | "lte" | "not_eq";

export type BalanceMonitor = {
  object: "balance_monitor";
  id: string;
  account_id: string;
  alert_condition: { field: MonitorField; operator: MonitorOperator; value: string };
  description: string;
  metadata: Metadata;
  version: number;
  triggered: boolean;
  created_at: string;
};

export type BulkStatus = "pending" | "processing" | "completed";

export type BulkRequest = {
  object: "bulk_request";
  id: string;
  idempotency_key: string;
  status: BulkStatus;
  total: number;
  processed: number;
  succeeded: number;
  failed: number;
  created_at: string;
  started_at: string | null;
  completed_at: string | null;
};

export type BulkResult = {
  object: "bulk_result";
  index: number;
  status: "pending" | "succeeded" | "failed";
  transaction_id: string | null;
  error: { code: string; detail: string } | null;
};

export type BatchResponse = {
  object: "batch";
  atomic: boolean;
  results: { transaction: Transaction | null; error: { code: string; detail: string } | null }[];
};

export type IntegrityReport = {
  object: "integrity_report";
  ok: boolean;
  issues: string[];
  chain_head: string;
};

export type Event = {
  object: "event";
  id: string;
  type: string;
  data: Record<string, unknown>;
  created_at: string;
};

export type WebhookEndpoint = {
  object: "webhook_endpoint";
  id: string;
  url: string;
  description: string;
  event_types: string[];
  enabled: boolean;
  version: number;
  created_at: string;
  secret?: string;
};

export type DeliveryStatus = "pending" | "succeeded" | "failed";

export type WebhookDelivery = {
  object: "webhook_delivery";
  id: string;
  endpoint_id: string;
  event_id: string;
  event_type: string;
  status: DeliveryStatus;
  attempts: number;
  next_attempt_at: string | null;
  last_attempt_at: string | null;
  last_status_code: number | null;
  last_error: string | null;
  delivered_at: string | null;
  created_at: string;
};

export type CreatedApiKey = ApiKey & { secret: string };
