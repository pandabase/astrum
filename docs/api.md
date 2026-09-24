# API reference

Astrum records money movements in a double-entry ledger. Use it to track balances, reserve funds, settle accounts and follow changes through events. Your application handles the actual bank, card or blockchain transfers.

The default base URL is `http://localhost:8080`. Follow the [quick start](../README.md#quick-start) to start a server and create your first API key.

For end-to-end application flows, see [multi-seller commerce](examples/commerce.md), [ride-hailing](examples/ride-hailing.md) and [usage-based billing](examples/usage-billing.md).

## Contents

- [Requests and responses](#requests-and-responses) · [Authentication](#authentication) · [Idempotency](#idempotency)
- [Ledgers](#ledgers) · [Currencies](#currencies) · [Accounts](#accounts) · [Transactions](#transactions) · [Entries](#entries)
- [Batches](#batches) · [Bulk requests](#bulk-requests) · [Holds](#holds) · [Scheduled transactions](#scheduled-transactions)
- [Statements](#statements) · [Settlements](#settlements) · [Account categories](#account-categories) · [Balance monitors](#balance-monitors)
- [Events](#events) · [Webhook endpoints](#webhook-endpoints) · [Webhook deliveries](#webhook-deliveries)
- [Health and integrity](#health-and-integrity) · [Errors](#errors) · [Server configuration](#server-configuration)

## Requests and responses

Send JSON bodies with `Content-Type: application/json`. Unknown body fields and trailing JSON are rejected. Successful responses use `application/json`; errors use `application/problem+json`.

```sh
export ASTRUM_URL='http://localhost:8080'
export ASTRUM_KEY='sk_...'

curl "$ASTRUM_URL/v1/ledgers" \
  -H "Authorization: Bearer $ASTRUM_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Payments"}'
```

Examples below show JSON request bodies. Replace abbreviated IDs such as `acct_...` with IDs returned by your server. Fields are optional unless marked **required**. Response tables describe the returned object, alongside the shared fields below.

### Shared fields and limits

| Value         | Format                                                                                                                                          |
| ------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| IDs           | TypeID strings with a resource prefix. The wrong prefix is rejected.                                                                            |
| Amounts       | Integer minor units as strings, up to 38 digits. `"100"` is USD 1.00. Entry, hold and capture amounts must be positive. Balances may be signed. |
| Times         | RFC 3339 strings, such as `2026-09-01T00:00:00Z`. Stored accounting times have microsecond precision.                                           |
| `name`        | Up to 255 UTF-8 bytes. Required names must contain non-whitespace text.                                                                         |
| `description` | Up to 1,024 UTF-8 bytes; defaults to `""`.                                                                                                      |
| `metadata`    | A JSON object, up to 16 KiB; defaults to `{}` on ledger resources that support it.                                                              |
| Request body  | Up to 1 MiB for regular JSON endpoints; bulk requests allow 16 MiB.                                                                             |
| JSON rules    | Field names are case-sensitive. Unknown fields, duplicate names and invalid UTF-8 are rejected with `400`.                                      |

Resource objects include `object`, `id` and `created_at` unless noted otherwise. `created_at` is the creation timestamp. Currency objects use `code` instead of `id`. Lists, balances, batch results, entries, deletion acknowledgements and integrity reports have their own shapes.

| Resource              | `object`                | ID prefix |
| --------------------- | ----------------------- | --------- |
| API key               | `api_key`               | `key_`    |
| Ledger                | `ledger`                | `ldg_`    |
| Account               | `account`               | `acct_`   |
| Transaction           | `transaction`           | `txn_`    |
| Hold                  | `hold`                  | `hold_`   |
| Scheduled transaction | `scheduled_transaction` | `sched_`  |
| Statement             | `statement`             | `stmt_`   |
| Settlement            | `settlement`            | `stl_`    |
| Account category      | `account_category`      | `cat_`    |
| Balance monitor       | `balance_monitor`       | `bm_`     |
| Bulk request          | `bulk_request`          | `blk_`    |
| Event                 | `event`                 | `evt_`    |
| Webhook endpoint      | `webhook_endpoint`      | `we_`     |
| Webhook delivery      | `webhook_delivery`      | `wd_`     |

### Pagination and filters

Every list accepts `limit` (1–100, default 25) and `cursor`. Pass the previous response’s `next_cursor` unchanged, keeping the same filters.

```json
{
  "object": "list",
  "data": [],
  "has_more": false,
  "next_cursor": null
}
```

Most resources list newest first by ID. Currencies sort by code; account entries and entry searches sort by increasing posting sequence; bulk results follow the input order. There is no general `sort` parameter or total-count field.

Where supported, use `metadata[key]=value` to match top-level string values. Up to 20 filters are allowed, one per key; all must match. For curl, use `--get --data-urlencode 'metadata[customer]=alice'`.

Accounting date filters use `effective_at_lower_bound` (inclusive) and `effective_at_upper_bound` (exclusive). If both are set, the upper bound must be later. They filter the accounting date, not the creation timestamp.

### Partial updates

Send only the fields you want to change. Metadata uses merge-patch behavior: keys you omit stay, and a key set to `null` is removed.

```json
{
  "description": "Primary wallet",
  "metadata": { "customer": "alice", "old_reference": null }
}
```

Only fields listed for an endpoint can be updated. An account’s currency or normal side, for example, can’t be changed through `PATCH`.

## Authentication

All API routes except `GET /healthz` require:

```http
Authorization: Bearer sk_...
```

| Role    | Access                                                                          |
| ------- | ------------------------------------------------------------------------------- |
| `read`  | GET/HEAD requests, except API keys and webhook endpoints.                       |
| `write` | Read and change resources, except API keys and webhook endpoints.               |
| `admin` | All routes, including API keys and webhook endpoints, which can expose secrets. |

Keys grant access across the server; they are not restricted to a ledger. Expired and revoked keys return `401`. Revocation reaches other instances within the 10-second authentication cache window. The last active admin key can't be revoked (`409 last_admin_key`); create another admin key first.

### API keys

| Method | Endpoint                   | Result                                                         |
| ------ | -------------------------- | -------------------------------------------------------------- |
| GET    | `/v1/me`                   | `200`: the key used for this request; available to every role. |
| POST   | `/v1/api_keys`             | `201`: a new key, including its secret. Admin only.            |
| GET    | `/v1/api_keys`             | `200`: paginated keys. Admin only.                             |
| GET    | `/v1/api_keys/{id}`        | `200`: one key. Admin only.                                    |
| POST   | `/v1/api_keys/{id}/revoke` | `200`: the revoked key. No body. Admin only.                   |

Create parameters:

| Field        | Type              | Description                                                       |
| ------------ | ----------------- | ----------------------------------------------------------------- |
| `name`       | string            | **Required.** A label for the key.                                |
| `role`       | string            | **Required.** `read`, `write` or `admin`; the API has no default. |
| `expires_at` | timestamp or null | Must be in the future if set. Omit for no expiry.                 |

```json
{
  "name": "Reporting service",
  "role": "read",
  "expires_at": "2030-01-01T00:00:00Z"
}
```

The response includes `name`, `role`, `hint`, `created_by` (key ID or null), `expires_at`, `revoked_at` and `last_used_at` (nullable timestamps). `secret` is present only when creating the key. Save it then; GET and list responses never return it.

## Idempotency

Use a unique `Idempotency-Key` for each intended operation. Reuse it when retrying that exact request.

```http
Idempotency-Key: order-1001-payment
```

It is **required** for these POST requests:

- `/v1/transactions`, `/v1/transactions/batch`, `/v1/transactions/{id}/reverse`
- `/v1/holds`, `/v1/holds/{id}/capture`
- `/v1/scheduled_transactions`, `/v1/bulk_requests`, `/v1/settlements`

Other POST, PATCH, PUT and DELETE requests can also use the header. It is not required for actions such as posting or archiving a pending transaction, voiding a hold, or canceling a schedule.

The HTTP replay cache is scoped to your API key and compares the method, full request URI and **raw body bytes**. Keep all three identical on retries, including JSON formatting. A replay returns the saved status and body with `Idempotent-Replayed: true`. Different requests using the same key return `422 idempotency_key_reused`; concurrent requests may return `409 idempotency_key_in_use` and should be retried later.

Responses below `500`, including validation errors, are cached for 24 hours from the first request. Responses that contain a secret (creating an API key or webhook endpoint) are sent with `Cache-Control: no-store` and never stored: a replay returns `409 idempotency_key_completed`, because the secret is shown only once. Server errors release the HTTP key for retry. Ledger operations also retain their own idempotency records beyond that cache window; those records are shared across API keys. Use globally unique operation keys, even when multiple clients use different API keys, and don’t recycle old keys.

Keys are limited to 255 bytes. Batch and bulk items append `/0`, `/1`, etc. to the supplied key, so leave room for that suffix.

## Ledgers

A ledger groups accounts that can transact with each other. Transactions cannot cross ledger boundaries.

| Method | Endpoint           | Result                                  |
| ------ | ------------------ | --------------------------------------- |
| POST   | `/v1/ledgers`      | `201`: ledger.                          |
| GET    | `/v1/ledgers`      | `200`: list; supports metadata filters. |
| GET    | `/v1/ledgers/{id}` | `200`: ledger.                          |
| PATCH  | `/v1/ledgers/{id}` | `200`: updated ledger.                  |

Create with **required** `name`, plus optional `description` and `metadata`. PATCH accepts the same fields.

```json
{
  "name": "Payments",
  "description": "Customer wallets",
  "metadata": { "region": "us" }
}
```

```json
{
  "object": "ledger",
  "id": "ldg_...",
  "name": "Payments",
  "description": "Customer wallets",
  "metadata": { "region": "us" },
  "version": 0,
  "created_at": "2026-09-01T00:00:00Z"
}
```

`version` tracks updates. Ledgers have no delete endpoint.

## Currencies

ISO 4217 currencies are preloaded. Register additional currencies before creating accounts that use them.

| Method | Endpoint                | Result                                        |
| ------ | ----------------------- | --------------------------------------------- |
| POST   | `/v1/currencies`        | `201`: currency.                              |
| GET    | `/v1/currencies`        | `200`: paginated currencies, ordered by code. |
| GET    | `/v1/currencies/{code}` | `200`: currency.                              |

| Create field | Type    | Description                                                                              |
| ------------ | ------- | ---------------------------------------------------------------------------------------- |
| `code`       | string  | **Required.** 3–16 characters: uppercase letters, digits or `_`, starting with a letter. |
| `exponent`   | integer | **Required.** 0–30. The number of decimal places in one whole unit.                      |

```json
{ "code": "ETH", "exponent": 18 }
```

Returns `object: "currency"`, `code`, `exponent` and `created_at`. Registration is permanent: there is no update or delete endpoint. Registering an existing code returns `409 currency_exists`.

## Accounts

An account holds one currency. Its normal side defines which entries increase its balance: `debit` for assets and expenses, usually `credit` for liabilities, equity and revenue.

| Method | Endpoint                     | Result                                                     |
| ------ | ---------------------------- | ---------------------------------------------------------- |
| POST   | `/v1/accounts`               | `201`: account.                                            |
| GET    | `/v1/accounts`               | `200`: paginated accounts.                                 |
| GET    | `/v1/accounts/{id}`          | `200`: account with current balances.                      |
| PATCH  | `/v1/accounts/{id}`          | `200`: account; accepts `name`, `description`, `metadata`. |
| POST   | `/v1/accounts/{id}/freeze`   | `200`: frozen account. No body.                            |
| POST   | `/v1/accounts/{id}/unfreeze` | `200`: open account. No body.                              |
| POST   | `/v1/accounts/{id}/close`    | `200`: closed account. No body.                            |
| GET    | `/v1/accounts/{id}/entries`  | `200`: paginated posted entries with running balances.     |
| GET    | `/v1/accounts/{id}/balances` | `200`: balances for an accounting date range.              |

### Create an account

| Field                 | Type      | Description                                                                    |
| --------------------- | --------- | ------------------------------------------------------------------------------ |
| `ledger_id`           | ledger ID | **Required.** The account’s ledger.                                            |
| `code`                | string    | **Required.** Nonblank, up to 128 bytes; unique in this ledger.                |
| `currency`            | string    | **Required.** A registered currency code.                                      |
| `normal_side`         | string    | **Required.** `debit` or `credit`.                                             |
| `name`, `description` | string    | Display name and description.                                                  |
| `metadata`            | object    | Your own attributes.                                                           |
| `allow_negative`      | boolean   | Default `false`. Allows unlimited negative balances within the amount range.   |
| `overdraft_limit`     | amount    | Default `"0"`. Nonnegative permitted overdraft when `allow_negative` is false. |

```json
{
  "ledger_id": "ldg_...",
  "code": "wallet:alice",
  "name": "Alice’s wallet",
  "currency": "USD",
  "normal_side": "credit"
}
```

List filters: `ledger_id`, `category_id`, `code` (exact match), `status` (`open`, `frozen`, `closed`), `currency`, and metadata filters.

### Account object and balances

The account response includes all create fields, plus:

| Field               | Type              | Meaning                                                                                        |
| ------------------- | ----------------- | ---------------------------------------------------------------------------------------------- |
| `currency_exponent` | integer           | Decimal places used to display amounts.                                                        |
| `status`            | string            | `open`, `frozen` or `closed`. New accounts are open.                                           |
| `balances`          | object            | `posted`, `pending` and `available`, each containing `debits`, `credits` and `amount` strings. |
| `held`              | amount            | Funds reserved by outstanding holds.                                                           |
| `lock_version`      | integer           | Account version for optimistic concurrency checks on entries.                                  |
| `status_changed_at` | timestamp or null | Last status change.                                                                            |

`posted` is the journal balance. `pending` adds pending debits and credits to posted totals. `available` subtracts pending outflows and holds from posted funds; pending inflows are not spendable yet. `amount` is expressed on the account’s normal side.

```json
{
  "posted": { "debits": "0", "credits": "5000", "amount": "5000" },
  "pending": { "debits": "1500", "credits": "5000", "amount": "3500" },
  "available": { "debits": "1500", "credits": "5000", "amount": "3500" }
}
```

This credit-normal wallet has $50 posted and $35 available after a pending $15 withdrawal.

Frozen accounts reject new money movements. Closing requires a zero posted balance, no pending balance changes and no held funds. Closed accounts cannot be reopened. Repeating the same status action returns the account unchanged.

### History

`GET /v1/accounts/{id}/entries` accepts pagination only. Each item has `object: "account_entry"`, `transaction_id`, `side`, `amount`, `currency`, `balance_after` and `created_at`. The running balance follows posting order, not `effective_at`.

`GET /v1/accounts/{id}/balances` accepts the two accounting date bounds. It returns `object: "balances"`, `account_id`, both bounds (nullable), and top-level `posted`, `pending`, `available` balance objects. These are entry totals for the selected range and **exclude holds**. Use the account object for the current spendable balance including holds.

## Transactions

Every transaction needs equal debits and credits **in each currency**. All entries must belong to one ledger. A transaction may contain several currencies, but one currency cannot balance another.

| Method | Endpoint                        | Result                                                         |
| ------ | ------------------------------- | -------------------------------------------------------------- |
| POST   | `/v1/transactions`              | `201`: transaction. Requires an idempotency key.               |
| GET    | `/v1/transactions`              | `200`: paginated transactions.                                 |
| GET    | `/v1/transactions/{id}`         | `200`: transaction.                                            |
| PATCH  | `/v1/transactions/{id}`         | `200`: updated pending transaction.                            |
| POST   | `/v1/transactions/{id}/post`    | `200`: posted transaction.                                     |
| POST   | `/v1/transactions/{id}/archive` | `200`: archived transaction. No body.                          |
| POST   | `/v1/transactions/{id}/reverse` | `201`: new reversing transaction. Requires an idempotency key. |

### Create a transaction

| Field                             | Type      | Description                                                                                                            |
| --------------------------------- | --------- | ---------------------------------------------------------------------------------------------------------------------- |
| `entries`                         | array     | **Required.** 2–1,000 entries, using the fields below.                                                                 |
| `status`                          | string    | `posted` (default) or `pending`.                                                                                       |
| `description`                     | string    | A readable explanation of the movement.                                                                                |
| `metadata`                        | object    | Your own attributes.                                                                                                   |
| `effective_at`                    | timestamp | Accounting date; defaults to creation time.                                                                            |
| `external_id`                     | string    | Up to 255 bytes. Unique within the ledger when nonempty.                                                               |
| `archive_on_balance_lock_failure` | boolean   | Default `false`. Save an archived transaction if a balance condition fails instead of returning `balance_lock_failed`. |

Each entry accepts:

| Field                      | Type             | Description                                                                           |
| -------------------------- | ---------------- | ------------------------------------------------------------------------------------- |
| `account_id`               | account ID       | **Required.** Account to debit or credit.                                             |
| `side`                     | string           | **Required.** `debit` or `credit`.                                                    |
| `amount`                   | amount           | **Required.** Positive integer minor units.                                           |
| `currency`                 | string           | Optional check; must match the account’s currency. Otherwise inferred.                |
| `lock_version`             | integer          | Nonnegative expected account version. A mismatch returns `409 lock_version_conflict`. |
| `pending_balance_amount`   | condition object | Required pending balance after the transaction.                                       |
| `posted_balance_amount`    | condition object | Required posted balance after the transaction.                                        |
| `available_balance_amount` | condition object | Required available balance after the transaction.                                     |

A condition can contain `gt`, `gte`, `eq`, `lt`, `lte` or `not_eq`, with amount strings as values. All supplied comparisons must pass. For example, `{"gte":"500"}` requires at least $5 available on a USD account after the whole transaction.

```json
{
  "description": "Opening capital",
  "external_id": "opening-2026",
  "entries": [
    { "account_id": "acct_cash...", "side": "debit", "amount": "10000" },
    { "account_id": "acct_equity...", "side": "credit", "amount": "10000" }
  ]
}
```

For a runnable flow that creates the accounts too, see the [wallet example](examples/wallet.md).

### Transaction object

| Field                      | Type                   | Meaning                                                                                                                                         |
| -------------------------- | ---------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `ledger_id`                | ledger ID              | Inferred from the entries.                                                                                                                      |
| `idempotency_key`          | string                 | Key used to create the transaction.                                                                                                             |
| `external_id`              | string or null         | Your unique reference.                                                                                                                          |
| `status`, `version`        | string, integer        | Current state and revision.                                                                                                                     |
| `description`, `metadata`  | string, object         | Transaction details.                                                                                                                            |
| `entries`                  | array                  | Account ID, side, amount and currency for each entry. Write responses may also include `resulting_balances`; don’t rely on it in GET responses. |
| `reverses_id`              | transaction ID or null | Original transaction if this is a reversal.                                                                                                     |
| `effective_at`             | timestamp              | Accounting date.                                                                                                                                |
| `posted_at`, `archived_at` | timestamp or null      | When the transaction reached that state.                                                                                                        |

List filters: `ledger_id`, `account_id`, `status` (`pending`, `posted`, `archived`), `external_id`, metadata, and the two accounting date bounds.

### Pending, posted and archived

Create with `status: "pending"` to reserve outgoing funds. Pending inflows increase the pending balance but not the available balance.

- **Edit:** PATCH a pending transaction with `description`, `metadata`, `effective_at` or `entries`. Supplied entries replace the entire pending set and must still balance within the original ledger.
- **Post:** POST to `/post` without a body to post all pending entries. To post less, send `{"entries":[...]}`. Amounts cannot exceed the pending totals for each account/side, and the result must balance. The unused reservation is released; nothing remains pending.
- **Archive:** POST to `/archive` to release the reservation without posting it.
- **Reverse:** POST to `/reverse` with optional `description` and `metadata`. This creates a posted transaction with opposite entries, leaving the original intact. Only posted transactions can be reversed, and each can be reversed once. Normal balance checks still apply.

Posting the same entries again, or archiving an already archived transaction, returns the existing result. Editing a posted or archived transaction returns `409 transaction_not_pending`.

## Entries

Use entry search to inspect individual debits and credits across accounts.

`GET /v1/entries` returns `200` with a paginated list.

| Filter                                      | Description                                  |
| ------------------------------------------- | -------------------------------------------- |
| `ledger_id`, `account_id`, `transaction_id` | Restrict to a resource.                      |
| `status`                                    | `posted` (default), `pending` or `archived`. |
| `side`                                      | `debit` or `credit`.                         |
| `statement_id`                              | Entries saved in a statement snapshot.       |
| `settlement_id`                             | Entries covered by a settlement.             |
| `settled`                                   | `true` or `false`.                           |
| Accounting date bounds                      | Filter by `effective_at`.                    |
| Metadata filters                            | Match the transaction’s metadata.            |

Each entry contains `object: "entry"`, `sequence` (string), `transaction_id`, `ledger_id`, `account_id`, `status`, `side`, `amount`, `currency`, `balance_after` (amount or null), `effective_at`, `created_at` and `settlement_id` (ID or null). Entries have no standalone ID or mutation endpoint. Pending and archived entries do not have a posted running balance.

## Batches

`POST /v1/transactions/batch` creates 1–1,000 transactions synchronously. Requires an idempotency key.

| Field          | Type    | Description                                                                               |
| -------------- | ------- | ----------------------------------------------------------------------------------------- |
| `transactions` | array   | **Required.** Normal transaction-create objects.                                          |
| `atomic`       | boolean | Default `true`: all transactions succeed or none do. Set `false` for independent results. |

```json
{
  "atomic": true,
  "transactions": [
    {
      "entries": [
        { "account_id": "acct_cash...", "side": "debit", "amount": "100" },
        { "account_id": "acct_equity...", "side": "credit", "amount": "100" }
      ]
    }
  ]
}
```

Returns `object: "batch"`, `atomic`, and `results` in input order. Each result has `transaction` (object or null) and `error` (null or `{"code":"...","detail":"..."}`). Item keys are `<Idempotency-Key>/<zero-based index>`.

All-success batches return `201`. Non-atomic batches with failures return `207`. Atomic failures use the relevant error status and a batch response; rolled-back items can have `batch_aborted`. Always inspect the per-item results, including on non-2xx batch responses.

## Bulk requests

Use bulk requests for larger jobs you don’t need to finish in one HTTP request. Items are independent, not atomic.

| Method | Endpoint                         | Result                                                   |
| ------ | -------------------------------- | -------------------------------------------------------- |
| POST   | `/v1/bulk_requests`              | `202`: queued bulk request. Requires an idempotency key. |
| GET    | `/v1/bulk_requests/{id}`         | `200`: progress.                                         |
| GET    | `/v1/bulk_requests/{id}/results` | `200`: paginated item results in input order.            |

The body is `{"transactions":[...]}`, with 1–10,000 transaction-create objects and a maximum size of 16 MiB. Item keys use the same suffix rule as batches. Invalid request structure can reject the job before it is queued; execution failures appear in item results.

| Bulk field                                  | Meaning                                                                               |
| ------------------------------------------- | ------------------------------------------------------------------------------------- |
| `idempotency_key`                           | Job’s request key.                                                                    |
| `status`                                    | `pending`, `processing` or `completed`. Completed does not mean every item succeeded. |
| `total`, `processed`, `succeeded`, `failed` | Integer counts.                                                                       |
| `started_at`, `completed_at`                | Nullable timestamps.                                                                  |

Each result has `object: "bulk_result"`, `index` (zero-based), `status` (`pending`, `succeeded`, `failed`), `transaction_id` (nullable) and `error` (nullable object with `code` and `detail`).

## Holds

A hold reserves available funds without creating a pending transaction. Capture it once, void it, or let it expire.

| Method | Endpoint                 | Result                                             |
| ------ | ------------------------ | -------------------------------------------------- |
| POST   | `/v1/holds`              | `201`: hold. Requires an idempotency key.          |
| GET    | `/v1/holds`              | `200`: list; filters `account_id`, `status`.       |
| GET    | `/v1/holds/{id}`         | `200`: hold.                                       |
| POST   | `/v1/holds/{id}/capture` | `200`: captured hold. Requires an idempotency key. |
| POST   | `/v1/holds/{id}/void`    | `200`: voided hold. No body.                       |

### Create a hold

| Field         | Type       | Description                                                                    |
| ------------- | ---------- | ------------------------------------------------------------------------------ |
| `account_id`  | account ID | **Required.** Account whose funds are reserved.                                |
| `amount`      | amount     | **Required.** Positive reservation amount.                                     |
| `expires_at`  | timestamp  | **Required.** When the hold becomes ineligible for capture. Use a future time. |
| `currency`    | string     | Optional currency check against the account.                                   |
| `description` | string     | What the reservation is for.                                                   |

```json
{
  "account_id": "acct_...",
  "amount": "10000",
  "description": "Fuel authorization",
  "expires_at": "2030-01-01T00:00:00Z"
}
```

The hold object includes those fields plus `idempotency_key`, `status` (`pending`, `captured`, `voided`, `expired`), `captured_amount` (nullable), `capture_transaction_id` (nullable) and `resolved_at` (nullable).

### Capture or release

Capture requires `destination_account_id` and a positive `amount` no greater than the hold. Optional `description` and `metadata` describe the resulting transaction.

```json
{
  "destination_account_id": "acct_...",
  "amount": "6240",
  "description": "Fuel purchase"
}
```

Capture decreases the held account on its normal side and creates the balancing entry on the destination, in the same ledger and currency. It releases the **whole** hold, including any unused amount. The response is the hold; fetch `capture_transaction_id` to inspect the transaction.

Expired holds cannot be captured, even before the expiry worker updates their status. The worker releases expired reservations in the background. Voiding releases a pending hold without a transaction; voiding it again returns the same result. See the [card authorization example](examples/card-authorization.md).

## Scheduled transactions

Schedule a transaction for the worker to execute at or after a given time. Scheduling does **not** reserve funds; account and balance checks happen when it executes.

| Method | Endpoint                                 | Result                                        |
| ------ | ---------------------------------------- | --------------------------------------------- |
| POST   | `/v1/scheduled_transactions`             | `201`: schedule. Requires an idempotency key. |
| GET    | `/v1/scheduled_transactions`             | `200`: list; filter `status`.                 |
| GET    | `/v1/scheduled_transactions/{id}`        | `200`: schedule.                              |
| POST   | `/v1/scheduled_transactions/{id}/cancel` | `200`: canceled schedule. No body.            |

Accepts the transaction-create fields plus **required** `execute_at`. `status` in the request is the desired _transaction_ status (`posted` by default), not the schedule status.

```json
{
  "execute_at": "2030-01-01T00:00:00Z",
  "description": "Monthly interest",
  "entries": [
    { "account_id": "acct_interest...", "side": "debit", "amount": "1000" },
    { "account_id": "acct_income...", "side": "credit", "amount": "1000" }
  ]
}
```

Returns `idempotency_key`, `execute_at`, `status` (`scheduled`, `executed`, `failed`, `canceled`), `description`, `metadata`, `entries`, `transaction_id` (nullable), `failure` (nullable string) and `resolved_at` (nullable). An executed schedule may have created a pending transaction if requested.

Only `scheduled` items can be canceled; repeating a cancellation is safe. There is no schedule edit or retry endpoint. Check `failure` on a failed schedule before creating a replacement with a new key.

## Statements

A statement saves an account’s posted opening balance, closing balance and entry set for a period. Later backdated transactions don’t change it.

| Method | Endpoint              | Result                            |
| ------ | --------------------- | --------------------------------- |
| POST   | `/v1/statements`      | `201`: statement.                 |
| GET    | `/v1/statements`      | `200`: list; filter `account_id`. |
| GET    | `/v1/statements/{id}` | `200`: statement.                 |

Create with **required** `account_id`, `effective_at_lower_bound` and `effective_at_upper_bound`, plus optional `description`. The lower bound is inclusive; the upper bound is exclusive.

```json
{
  "account_id": "acct_...",
  "effective_at_lower_bound": "2026-09-01T00:00:00Z",
  "effective_at_upper_bound": "2026-10-01T00:00:00Z",
  "description": "September statement"
}
```

Returns `ledger_id`, `account_id`, `currency`, `description`, both bounds, `starting_balance`, `ending_balance` and `entry_count`. Each balance contains `debits`, `credits` and `amount` strings. Retrieve the saved entries through `GET /v1/entries?statement_id=stmt_...`.

## Settlements

A settlement records the net movement from an account’s unsettled postings to a contra account and marks the covered entries. Each posting can be settled only once.

| Method | Endpoint               | Result                                                 |
| ------ | ---------------------- | ------------------------------------------------------ |
| POST   | `/v1/settlements`      | `201`: settlement. Requires an idempotency key.        |
| GET    | `/v1/settlements`      | `200`: list; `account_id` filters the settled account. |
| GET    | `/v1/settlements/{id}` | `200`: settlement.                                     |

| Field                      | Type           | Description                                                                              |
| -------------------------- | -------------- | ---------------------------------------------------------------------------------------- |
| `settled_account_id`       | account ID     | **Required.** Account whose unsettled entries are covered.                               |
| `contra_account_id`        | account ID     | **Required.** Different account in the same ledger and currency.                         |
| `effective_at_upper_bound` | timestamp      | Only cover postings before this accounting date. Omit to include all unsettled postings. |
| `description`, `metadata`  | string, object | Settlement details.                                                                      |

```json
{
  "settled_account_id": "acct_vendor...",
  "contra_account_id": "acct_bank...",
  "description": "Vendor payout"
}
```

The response includes the create fields, `idempotency_key`, `ledger_id`, `currency`, signed `amount`, `entry_count` and `transaction_id`. A zero-net settlement can cover entries without creating a transaction, so `transaction_id` may be null. Use `GET /v1/entries?settlement_id=stl_...` to inspect covered entries. See the [marketplace example](examples/marketplace.md).

## Account categories

Categories combine account balances within one ledger and currency. Nested categories can be up to seven levels deep, with no cycles. Accounts reached through multiple branches are counted once.

| Method | Endpoint                                          | Result                                                                  |
| ------ | ------------------------------------------------- | ----------------------------------------------------------------------- |
| POST   | `/v1/account_categories`                          | `201`: category.                                                        |
| GET    | `/v1/account_categories`                          | `200`: paginated categories.                                            |
| GET    | `/v1/account_categories/{id}`                     | `200`: category and rolled-up balances. Accepts accounting date bounds. |
| PATCH  | `/v1/account_categories/{id}`                     | `200`: category; accepts `name`, `description`, `metadata`.             |
| DELETE | `/v1/account_categories/{id}`                     | `200`: deletion acknowledgement. Accounts remain.                       |
| PUT    | `/v1/account_categories/{id}/accounts/{member}`   | `200`: category after adding an account. No body.                       |
| DELETE | `/v1/account_categories/{id}/accounts/{member}`   | `200`: category after removing an account. No body.                     |
| PUT    | `/v1/account_categories/{id}/categories/{member}` | `200`: category after adding a child category. No body.                 |
| DELETE | `/v1/account_categories/{id}/categories/{member}` | `200`: category after removing a child category. No body.               |

Create with **required** `ledger_id`, `currency`, `normal_side` (`debit` or `credit`) and `name`. `description` and `metadata` are optional.

```json
{
  "ledger_id": "ldg_...",
  "currency": "USD",
  "normal_side": "credit",
  "name": "Customer wallets"
}
```

Returns the create fields, `version` and `balances` (`posted`, `pending`, `available`). The category’s normal side controls the sign of its totals; members must match its ledger and currency. Date-filtered balances exclude holds; unfiltered balances include them.

List filters: `ledger_id`, `parent_id`, `account_id`, metadata. Use `GET /v1/accounts?category_id=cat_...` to list accounts in a category, including nested members. Adding an existing membership or removing an absent one is safe.

Deletion returns `{"object":"account_category","id":"cat_...","deleted":true}`.

## Balance monitors

Watch an account for a balance condition, such as available funds falling below a threshold. A `balance_monitor.triggered` event is emitted when the condition changes from false to true during a balance change.

| Method | Endpoint                    | Result                                                        |
| ------ | --------------------------- | ------------------------------------------------------------- |
| POST   | `/v1/balance_monitors`      | `201`: monitor.                                               |
| GET    | `/v1/balance_monitors`      | `200`: list; filter `account_id`.                             |
| GET    | `/v1/balance_monitors/{id}` | `200`: monitor.                                               |
| PATCH  | `/v1/balance_monitors/{id}` | `200`: monitor; only `description` and `metadata` can change. |
| DELETE | `/v1/balance_monitors/{id}` | `200`: deletion acknowledgement.                              |

| Create field              | Type           | Description                                             |
| ------------------------- | -------------- | ------------------------------------------------------- |
| `account_id`              | account ID     | **Required.** Account to watch.                         |
| `alert_condition`         | object         | **Required.** `field`, `operator` and `value` as below. |
| `description`, `metadata` | string, object | Your label and attributes.                              |

`field` is `pending_balance_amount`, `posted_balance_amount` or `available_balance_amount`. `operator` is `gt`, `gte`, `eq`, `lt`, `lte` or `not_eq`. `value` is an amount string.

```json
{
  "account_id": "acct_...",
  "alert_condition": {
    "field": "available_balance_amount",
    "operator": "lt",
    "value": "500"
  },
  "description": "Low wallet balance"
}
```

Returns the create fields plus `version` and `triggered` (whether the condition currently holds). Trigger events also include the balances at the crossing. Creating a monitor whose condition already holds does not itself emit a crossing event. To change the account or condition, replace the monitor.

Deletion returns `{"object":"balance_monitor","id":"bm_...","deleted":true}`.

## Events

Events are saved in the same database transaction as the change they describe.

| Method | Endpoint          | Result                                            |
| ------ | ----------------- | ------------------------------------------------- |
| GET    | `/v1/events`      | `200`: list; `type` matches one exact event type. |
| GET    | `/v1/events/{id}` | `200`: event.                                     |

An event has `object: "event"`, `id`, `type`, `data` (the resource snapshot) and `created_at`. `data` is the resource itself, not a `data.object` wrapper containing another object.

| Type                                                           | Data                                                      |
| -------------------------------------------------------------- | --------------------------------------------------------- |
| `account.created`, `account.updated`                           | Account; updates include status changes.                  |
| `transaction.created`                                          | Newly created transaction, including ones created posted. |
| `transaction.updated`                                          | Edited pending transaction.                               |
| `transaction.posted`                                           | Pending transaction posted.                               |
| `transaction.archived`                                         | Transaction archived.                                     |
| `hold.created`, `hold.captured`, `hold.voided`, `hold.expired` | Hold.                                                     |
| `balance_monitor.triggered`                                    | Monitor, including `balances`.                            |
| `bulk_request.completed`                                       | Bulk request with final counts.                           |
| `settlement.created`                                           | Settlement.                                               |

Transaction event entries omit `resulting_balances`. Events are snapshots, so fetch the resource if you need its current state.

Events and their delivery logs are eligible for deletion after `EVENT_RETENTION` (default 30 days), once dispatch has processed them and no delivery remains pending. Failed deliveries do not prevent retention cleanup. The event feed is not a permanent journal.

## Webhook endpoints

Register a URL to receive events as JSON POST requests. Endpoint routes are admin only.

| Method | Endpoint                     | Result                              |
| ------ | ---------------------------- | ----------------------------------- |
| POST   | `/v1/webhook_endpoints`      | `201`: endpoint and signing secret. |
| GET    | `/v1/webhook_endpoints`      | `200`: paginated endpoints.         |
| GET    | `/v1/webhook_endpoints/{id}` | `200`: endpoint without secret.     |
| PATCH  | `/v1/webhook_endpoints/{id}` | `200`: updated endpoint.            |
| DELETE | `/v1/webhook_endpoints/{id}` | `200`: deletion acknowledgement.    |

| Field         | Type         | Description                                                                                                                            |
| ------------- | ------------ | -------------------------------------------------------------------------------------------------------------------------------------- |
| `url`         | string       | **Required on create.** Absolute HTTPS URL, no credentials, up to 2,048 bytes. `localhost` and loopback, private and link-local IPs are rejected, and delivery re-checks the address it connects to; `WEBHOOK_ALLOW_INSECURE` lifts both. |
| `description` | string       | Up to 1,024 bytes.                                                                                                                     |
| `event_types` | string array | Up to 64 patterns. Omit or use `[]` for all events. Supports exact names, `transaction.*` and `*`.                                     |
| `enabled`     | boolean      | Default `true`. Disabled endpoints receive no new dispatches or delivery attempts while disabled.                                      |

PATCH accepts the same fields, all optional.

```json
{
  "url": "https://example.com/webhooks/astrum",
  "event_types": ["transaction.*", "hold.captured"],
  "description": "Payments service"
}
```

The response includes these fields and `version`. Creation also returns `secret`, beginning with `whsec_`; save it then. The secret is absent on subsequent reads. There is no secret rotation endpoint; create a replacement endpoint if needed.

Deletion returns `{"object":"webhook_endpoint","id":"we_...","deleted":true}` and removes that endpoint’s delivery records.

### Receiving and verifying events

Delivery is **at least once**, with no ordering guarantee. Deduplicate by event ID and return a `2xx` response after accepting the event. Any other status or network error is a failed attempt. Requests time out after 10 seconds; redirects are not followed.

```http
Content-Type: application/json
User-Agent: Astrum-Webhooks/1
Astrum-Event-Id: evt_...
Astrum-Signature: t=1788220800,v1=<hex signature>
```

The body is the event object described above. To verify the signature:

1. Read the raw request bytes before parsing JSON.
2. Extract `t` and `v1` from `Astrum-Signature`.
3. Compute HMAC-SHA256 over `<t>.<raw body>`, using the **entire secret string**, including `whsec_`, as the key. Do not base64-decode it.
4. Compare the lowercase hex digest with `v1` using a constant-time comparison.
5. Reject timestamps outside your tolerance, such as five minutes, and deduplicate the event ID.

There are 11 automatic retries after the initial attempt. Delays are 1 minute, 5 minutes, 30 minutes, 1 hour, 2 hours, 5 hours, 10 hours, 10 hours, 12 hours, 12 hours and 12 hours—roughly three days in total. Each attempt receives a fresh timestamp and signature.

## Webhook deliveries

Inspect failures or request another attempt.

| Method | Endpoint                            | Result                                                    |
| ------ | ----------------------------------- | --------------------------------------------------------- |
| GET    | `/v1/webhook_deliveries`            | `200`: list; filters `endpoint_id`, `event_id`, `status`. |
| GET    | `/v1/webhook_deliveries/{id}`       | `200`: delivery.                                          |
| POST   | `/v1/webhook_deliveries/{id}/retry` | `200`: delivery after requesting retry. No body.          |

| Field                                   | Type              | Meaning                                    |
| --------------------------------------- | ----------------- | ------------------------------------------ |
| `endpoint_id`, `event_id`, `event_type` | strings           | Destination and event.                     |
| `status`                                | string            | `pending`, `succeeded` or `failed`.        |
| `attempts`                              | integer           | Attempts made so far.                      |
| `next_attempt_at`                       | timestamp or null | Next attempt for a pending delivery.       |
| `last_attempt_at`                       | timestamp or null | Most recent attempt.                       |
| `last_status_code`                      | integer or null   | HTTP response status, if one was received. |
| `last_error`                            | string or null    | Most recent failure.                       |
| `delivered_at`                          | timestamp or null | Successful delivery time.                  |

Retry marks an unsuccessful delivery pending and due now; the worker sends it asynchronously. It does not reset the attempt count. Retrying a succeeded delivery leaves it unchanged. A disabled endpoint must be enabled before the worker can send its pending deliveries.

## Health and integrity

| Method | Endpoint        | Result                                                                   |
| ------ | --------------- | ------------------------------------------------------------------------ |
| GET    | `/healthz`      | Public. `200 {"status":"ok"}` if the database responds; `503` otherwise. |
| GET    | `/v1/integrity` | Authenticated. `200` with a balance and seal-chain verification report.  |

```json
{
  "object": "integrity_report",
  "ok": true,
  "issues": [],
  "chain_head": "<chain head>"
}
```

Check `ok` and `issues`, not just the HTTP status. An integrity failure is returned as a report; an inability to run the check returns an error. The health endpoint checks database reachability only.

## Errors

Use `code` for application logic and `detail` for a readable explanation. `X-Request-ID` is returned in the response headers, and problem responses include `request_id` for finding the request in logs.

```json
{
  "type": "urn:astrum:error:insufficient_funds",
  "title": "Unprocessable Entity",
  "status": 422,
  "code": "insufficient_funds",
  "detail": "ledger: insufficient funds",
  "request_id": "<request UUID>"
}
```

| Status | Codes                                                                                                                                                                                                                                                                                                     |
| ------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 400    | `invalid_request`, `idempotency_key_required`                                                                                                                                                                                                                                                             |
| 401    | `unauthorized`                                                                                                                                                                                                                                                                                            |
| 403    | `forbidden`                                                                                                                                                                                                                                                                                               |
| 404    | `not_found`                                                                                                                                                                                                                                                                                               |
| 409    | `lock_version_conflict`, `currency_exists`, `account_exists`, `transaction_not_pending`, `transaction_not_posted`, `external_id_exists`, `account_not_empty`, `already_reversed`, `hold_not_pending`, `schedule_not_pending`, `idempotency_key_in_use`, `idempotency_key_completed`, `last_admin_key`      |
| 413    | `request_too_large`                                                                                                                                                                                                                                                                                       |
| 422    | `validation_error`, `category_cycle`, `category_too_deep`, `category_mismatch`, `balance_lock_failed`, `unknown_ledger`, `unknown_currency`, `cross_ledger_transaction`, `insufficient_funds`, `unbalanced_transaction`, `account_not_open`, `amount_overflow`, `batch_aborted`, `idempotency_key_reused` |
| 500    | `internal_error`                                                                                                                                                                                                                                                                                          |
| 503    | `service_unavailable`                                                                                                                                                                                                                                                                                     |
| 504    | `timeout`                                                                                                                                                                                                                                                                                                 |

Malformed JSON, unknown body fields and invalid TypeIDs return `400`; valid JSON that violates a business rule generally returns `422`. Body-size failures from a JSON decoder currently return `400 invalid_request`; the idempotency middleware returns `413 request_too_large` when its 16 MiB limit is exceeded. Unknown routes or unsupported methods may use the HTTP router’s plain-text errors instead of a problem object.

For a timeout or connection loss, retry the identical request with the same idempotency key. For a changed request, use a new key. Batch responses are the exception to the problem-object format: inspect their per-item results.

## Server configuration

Astrum reads environment variables directly; it does not load `.env` files. Migrations run on startup.

| Variable                     | Default  | Purpose                                                                                                           |
| ---------------------------- | -------- | ----------------------------------------------------------------------------------------------------------------- |
| `DATABASE_URL`               | Required | PostgreSQL connection URL.                                                                                        |
| `LEDGER_SEAL_KEY`            | Required | At least 32 bytes. Keep outside the database and reuse across restarts; startup checks it against the seal chain. |
| `HTTP_ADDR`                  | `:8080`  | API listen address.                                                                                               |
| `LOG_LEVEL`                  | `info`   | Logging level.                                                                                                    |
| `LOG_FORMAT`                 | `text`   | `text`, `json` or `logfmt`.                                                                                       |
| `DB_MAX_CONNS`               | `32`     | Maximum database connections.                                                                                     |
| `LEDGER_WORKERS`             | `8`      | Must be below `DB_MAX_CONNS`.                                                                                     |
| `LEDGER_MAX_BATCH`           | `256`    | Worker batch size; separate from API batch limits.                                                                |
| `LEDGER_BATCH_CONCURRENCY`   | `4`      | Batch requests committed at once; others queue so overlapping batches don't wait on each other's account locks.   |
| `DB_ALLOW_UNSAFE_DURABILITY` | `false`  | Allow unsafe database durability settings for local development.                                                  |
| `EVENT_RETENTION`            | `720h`   | Retention for dispatched events without pending deliveries.                                                       |
| `WEBHOOK_ALLOW_INSECURE`     | `false`  | Allow HTTP and private-network webhook URLs for local development.                                                |
| `WEB_DIR`                    | Unset    | Built web interface to serve, such as `web/build/client`.                                                         |

### Development and benchmarks

Run backend tests with `ASTRUM_TEST_DATABASE_URL='postgres://...' go test ./...`; database integration tests are skipped if the variable is absent. See [web/README.md](../web/README.md) for frontend commands.

To benchmark a running API with a write or admin key:

```sh
export ASTRUM_KEY='sk_...'
go run ./cmd/astrum-bench -scenario transfer -duration 30s -concurrency 32 -verify
```

The benchmark creates its own ledger and accounts. Scenarios are `transfer`, `batch`, `pending`, `hold` and `read`. Run `go run ./cmd/astrum-bench -h` for all options.
