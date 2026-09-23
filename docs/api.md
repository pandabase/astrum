# API reference

Astrum’s API accepts JSON. Send your API key in the `Authorization` header on every request except `GET /healthz`:

```http
Authorization: Bearer sk_...
```

If you haven’t started the server or created a key yet, follow the [quick start](../README.md#quick-start). For a complete flow, try the [wallet example](examples/wallet.md).

## Making requests

### API keys

Each key has a role. A `read` key can make GET requests, a `write` key can also make changes, and an `admin` key can manage other keys. Managing keys always requires the `admin` role. Revoked keys stop working across all instances within 10 seconds.

### IDs and amounts

Resources use [TypeIDs](https://github.com/jetify-com/typeid), such as `acct_01h455vb4pex5vsknk084sn02q`. The prefix tells you what kind of resource the ID belongs to. Astrum rejects an ID if its kind doesn’t match the request.

Send money amounts as strings containing integer minor units, with up to 38 digits. For USD, `"100"` means 100 cents, or $1.00.

### Retrying a request

Requests that move money need an `Idempotency-Key` header. Reuse the key when retrying the same request, and Astrum returns the original result. If you reuse it with a different body, you’ll get `422 idempotency_key_reused`.

Idempotency keys are scoped to your API key, so use the same API key for retries too.

### Lists and pagination

List endpoints return this shape:

```json
{
  "object": "list",
  "data": [],
  "has_more": true,
  "next_cursor": "..."
}
```

Pass `next_cursor` as the `cursor` parameter to fetch the next page. You can set `limit` from 1 to 100; the default is 25. Endpoints that support metadata filters accept `metadata[key]=value`.

### Updating resources

Use `PATCH` to update fields such as `name`, `description` and `metadata` where supported. Metadata follows [JSON merge patch](https://www.rfc-editor.org/rfc/rfc7396): existing keys stay unless you replace them, and `null` removes a key.

## How the ledger works

### Ledgers, currencies and accounts

A ledger keeps a set of accounts together. Transactions can’t cross ledger boundaries, and each account’s `code` must be unique within its ledger.

Every account has a currency. ISO 4217 currencies are preloaded; register custom currencies such as `ETH` before using them. A currency’s exponent defines its minor unit and can’t be changed after registration.

Accounts also have a normal side: the side that increases their balance. Assets and expenses are usually debit-normal, while liabilities, equity and revenue are usually credit-normal. Balances can’t fall below zero unless the account’s `allow_negative` or `overdraft_limit` setting permits it.

### Reading balances

Each account exposes three balance views, with `debits`, `credits` and an `amount` expressed on its normal side:

| Balance | What it tells you |
| --- | --- |
| `posted` | What’s been recorded in the journal. |
| `pending` | The posted balance plus pending entries. |
| `available` | What’s available to spend: posted funds minus pending outflows and holds. |

### Transactions and holds

Every transaction must have equal debits and credits in each currency. Transactions are `posted` by default. Use `pending` when you need to reserve funds before completing a transfer: pending outflows reduce the available balance immediately. You can later post the transaction in full or in part, or archive it to release the funds.

Use `effective_at` for the accounting date and `external_id` for your own unique reference.

A hold reserves part of an account’s available balance without posting a transaction. Capture it when the payment completes, void it when it’s cancelled, or let it expire. The [card authorization example](examples/card-authorization.md) walks through this flow.

### Balance checks

A balance lock lets you say, for example, “only send this payment if at least $5 remains available.” Add `pending_balance_amount`, `posted_balance_amount` or `available_balance_amount` to an entry, with one of these comparisons: `gt`, `gte`, `eq`, `lt`, `lte` or `not_eq`. Astrum checks the balance after applying the whole transaction.

Use `lock_version` when a transaction should proceed only if the account hasn’t changed since you read it. Set `archive_on_balance_lock_failure` to keep a failed balance check as an archived transaction instead of returning an error.

Balance monitors let you watch for changes over time. They emit `balance_monitor.triggered` when a balance moves into the condition you configured.

### Statements and categories

Statements save an account’s opening balance, closing balance and entries for an `effective_at` range. Once created, a statement stays the same even if you later post backdated transactions.

Categories combine balances from accounts in the same ledger and currency. You can nest categories up to seven levels deep.

### Settlements

A settlement transfers an account’s unsettled net balance to another account, called the contra account. It also marks the entries it covered, so an entry can’t be settled twice. See the [marketplace example](examples/marketplace.md) for vendor payouts and processor deposits.

### Sending many transactions

Use `/v1/transactions/batch` for up to 1,000 transactions in one request. By default, they all succeed or none do.

For larger jobs, bulk requests process up to 10,000 independent transactions in the background. You can check progress and retrieve each item’s result. Item `i` uses the idempotency key `<Idempotency-Key>/<i>`.

## Events and webhooks

Astrum writes each event in the same database transaction as the change it describes. Webhooks send these events to public HTTPS URLs. Delivery is at least once, and events may arrive out of order, so your handler should expect duplicates and handle ordering itself. Failed deliveries are retried for about three days.

Each request includes an `Astrum-Signature` header:

```text
t=<unix>,v1=<hex HMAC-SHA256 of "t.body">
```

Here, `t` is the Unix timestamp and `body` is the request body. Delivered events are deleted after `EVENT_RETENTION`.

## Routes

| Method | Path | Description |
| --- | --- | --- |
| GET | `/healthz` | Database reachable |
| GET | `/v1/me` | The calling key, with its role (any key) |
| POST | `/v1/api_keys` | Create a key; the secret is returned once (admin) |
| GET | `/v1/api_keys`, `/v1/api_keys/{id}` | List or get keys (admin) |
| POST | `/v1/api_keys/{id}/revoke` | Revoke a key (admin) |
| POST, GET | `/v1/currencies` | Register or list currencies |
| GET | `/v1/currencies/{code}` | Get a currency |
| POST, GET | `/v1/ledgers` | Create or list ledgers |
| GET, PATCH | `/v1/ledgers/{id}` | Get or update a ledger |
| POST, GET | `/v1/accounts` | Create or list accounts |
| GET, PATCH | `/v1/accounts/{id}` | Get or update an account |
| POST | `/v1/accounts/{id}/freeze`, `/unfreeze`, `/close` | Change account status |
| GET | `/v1/accounts/{id}/entries` | Entries with running balance |
| GET | `/v1/accounts/{id}/balances` | Balances over an `effective_at` range |
| GET | `/v1/entries` | Search entries |
| POST, GET | `/v1/transactions` | Create or list transactions |
| POST | `/v1/transactions/batch` | Create up to 1,000 transactions |
| GET, PATCH | `/v1/transactions/{id}` | Get or edit a pending transaction |
| POST | `/v1/transactions/{id}/post`, `/archive`, `/reverse` | Post, archive or reverse |
| POST | `/v1/holds` | Create a hold |
| GET | `/v1/holds` | List holds, filter by `account_id` or `status` |
| GET | `/v1/holds/{id}` | Get a hold |
| POST | `/v1/holds/{id}/capture`, `/void` | Capture or void a hold |
| POST | `/v1/scheduled_transactions` | Post a transaction at `execute_at` |
| GET | `/v1/scheduled_transactions` | List scheduled transactions, filter by `status` |
| GET | `/v1/scheduled_transactions/{id}` | Get a scheduled transaction |
| POST | `/v1/scheduled_transactions/{id}/cancel` | Cancel it |
| POST, GET | `/v1/statements` | Create or list statements |
| GET | `/v1/statements/{id}` | Get a statement |
| POST, GET | `/v1/account_categories` | Create or list categories |
| GET, PATCH, DELETE | `/v1/account_categories/{id}` | Get with balances, update or delete |
| PUT, DELETE | `/v1/account_categories/{id}/accounts/{account_id}` | Add or remove an account |
| PUT, DELETE | `/v1/account_categories/{id}/categories/{child_id}` | Nest or unnest a category |
| POST, GET | `/v1/settlements` | Create or list settlements |
| GET | `/v1/settlements/{id}` | Get a settlement |
| POST | `/v1/bulk_requests` | Queue up to 10,000 transactions |
| GET | `/v1/bulk_requests/{id}`, `/results` | Progress and per-item results |
| POST, GET | `/v1/balance_monitors` | Create or list monitors |
| GET, PATCH, DELETE | `/v1/balance_monitors/{id}` | Get, update or delete a monitor |
| GET | `/v1/events`, `/v1/events/{id}` | List or get events |
| POST, GET | `/v1/webhook_endpoints` | Create or list endpoints; the secret is returned once |
| GET, PATCH, DELETE | `/v1/webhook_endpoints/{id}` | Get, update or delete an endpoint |
| GET | `/v1/webhook_deliveries`, `/v1/webhook_deliveries/{id}` | Delivery log |
| POST | `/v1/webhook_deliveries/{id}/retry` | Retry a delivery now |
| GET | `/v1/integrity` | Recheck balances and the seal chain |

## Errors

Errors use the [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) problem format. Use the stable `code` to decide how your application should respond, and the `request_id` to find the request in logs.

```json
{
  "type": "urn:astrum:error:insufficient_funds",
  "title": "Unprocessable Entity",
  "status": 422,
  "code": "insufficient_funds",
  "detail": "...",
  "request_id": "..."
}
```

| Status | Codes |
| --- | --- |
| 400 | `invalid_request`, `idempotency_key_required` |
| 401 | `unauthorized` |
| 403 | `forbidden` |
| 404 | `not_found` |
| 409 | `lock_version_conflict`, `currency_exists`, `account_exists`, `transaction_not_pending`, `transaction_not_posted`, `external_id_exists`, `account_not_empty`, `already_reversed`, `hold_not_pending`, `schedule_not_pending`, `idempotency_key_in_use` |
| 413 | `request_too_large` |
| 422 | `validation_error`, `category_cycle`, `category_too_deep`, `category_mismatch`, `balance_lock_failed`, `unknown_ledger`, `unknown_currency`, `cross_ledger_transaction`, `insufficient_funds`, `unbalanced_transaction`, `account_not_open`, `amount_overflow`, `batch_aborted`, `idempotency_key_reused` |
| 5xx | `internal_error`, `service_unavailable`, `timeout` |
