<p align="center">
  <img src="assets/astrum-logo.png" alt="Astrum" width="420">
</p>

<p align="center"><strong>A financial kernel and operating system for money.</strong></p>

Astrum is for apps that need to keep track of money. It handles accounts, balances, and transactions using a double-entry ledger. You can use it for a wallet, a marketplace, a billing system, or anywhere you need a record of who owns what.

[API reference](docs/api.md) · [Examples](#examples) · [Releases](https://github.com/pandabase/astrum/releases)

## Why we built it

Our old ledger at Pandabase was a simple TypeScript implementation without double-entry accounting. We built Astrum to replace it and chose Go for performance. That rewrite gave us a chance to put proper accounting rules at the core of the system.

Astrum records money movement as balanced debits and credits, with integer amounts to avoid floating-point rounding errors. Holds, pending transactions, and reversals have explicit behavior in the ledger. Idempotent requests make retries safe, and the journal keeps a record we can inspect when something doesn't add up.

We use Astrum to keep track of money across our services. Internally, we run a custom version with plugins that sync data and reconcile records with other systems. Those integrations are part of how we use Astrum day to day, and we plan to open-source the plugins soon.

## Features

- Double-entry accounting with integer amounts and multiple currencies.
- Pending and available balances, holds, and overdraft limits.
- Reversals, scheduled transactions, atomic batches, and bulk jobs.
- Settlements, statements, account categories, and balance monitors.
- Role-based API keys, idempotent requests, and signed webhooks.
- A web dashboard and journal history that lets you detect tampering.

## Quick start

You'll need Go 1.27.1 and a PostgreSQL database named `astrum`.

Generate a seal key with `openssl rand -hex 32`. Save it outside the database and reuse it each time you start Astrum.

```sh
export DATABASE_URL='postgres://postgres:postgres@localhost:5432/astrum?sslmode=disable'
export LEDGER_SEAL_KEY='<your saved seal key>'

go run ./cmd/astrum keys create -name ops
go run ./cmd/astrum
```

The first command prints an admin API key. Save it before continuing. You won't be able to see it again.

Migrations run automatically when the server starts. Astrum doesn't load `.env` files, so export the variables in your shell as shown above.

## Usage

In another terminal, set `ASTRUM_KEY` to the API key you saved and create a ledger:

```sh
export ASTRUM_KEY='sk_...'

curl http://localhost:8080/v1/ledgers \
  -H "Authorization: Bearer $ASTRUM_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Payments"}'
```

Then list your ledgers:

```sh
curl http://localhost:8080/v1/ledgers \
  -H "Authorization: Bearer $ASTRUM_KEY"
```

### Create two accounts

Let's record a $25 sale. You'll need a cash account and a sales revenue account. Replace `ldg_...` below with the ledger ID from the response above.

```sh
curl http://localhost:8080/v1/accounts \
  -H "Authorization: Bearer $ASTRUM_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "ledger_id": "ldg_...",
    "code": "cash",
    "currency": "USD",
    "normal_side": "debit"
  }'

curl http://localhost:8080/v1/accounts \
  -H "Authorization: Bearer $ASTRUM_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "ledger_id": "ldg_...",
    "code": "sales",
    "currency": "USD",
    "normal_side": "credit"
  }'
```

Save both account IDs. Debits increase the cash account, and credits increase sales revenue.

### Record the sale

Replace `acct_cash...` and `acct_sales...` with those IDs. USD amounts are in cents, so `"2500"` means $25.

```sh
curl http://localhost:8080/v1/transactions \
  -H "Authorization: Bearer $ASTRUM_KEY" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: sale-001' \
  -d '{
    "description": "Sale #001",
    "entries": [
      {"account_id": "acct_cash...", "side": "debit", "amount": "2500"},
      {"account_id": "acct_sales...", "side": "credit", "amount": "2500"}
    ]
  }'
```

The transaction posts immediately. Cash and sales revenue each increase by $25, with equal debits and credits. This records a payment you've already received through your payment provider.

Reuse `sale-001` when retrying this exact request. Use a new key for each new sale.

### Check the balance and entries

Replace `acct_cash...` with your cash account ID:

```sh
curl http://localhost:8080/v1/accounts/acct_cash... \
  -H "Authorization: Bearer $ASTRUM_KEY"

curl http://localhost:8080/v1/accounts/acct_cash.../entries \
  -H "Authorization: Bearer $ASTRUM_KEY"
```

The account's `balances.posted.amount` is `"2500"`. Its entries show the debit from the sale.

For transfers and withdrawals, follow the [wallet example](docs/examples/wallet.md). The [API reference](docs/api.md) covers the rest.

## Dashboard

Stop the server, build the dashboard, and restart with `WEB_DIR` set:

```sh
pnpm -C web install
pnpm -C web build
WEB_DIR=web/build/client go run ./cmd/astrum
```

Open [localhost:8080](http://localhost:8080) and sign in with your API key.

You can also download a [release](https://github.com/pandabase/astrum/releases) for Linux or macOS. Each archive includes the server, benchmark tool, dashboard, and checksums. Extract it, set the same environment variables, and run `WEB_DIR=web ./astrum` from the extracted directory.

## Examples

Start with a [wallet](docs/examples/wallet.md), [marketplace](docs/examples/marketplace.md), or [card authorization](docs/examples/card-authorization.md).

The [commerce](docs/examples/commerce.md), [ride-hailing](docs/examples/ride-hailing.md), and [usage billing](docs/examples/usage-billing.md) examples show how the pieces fit together in larger apps. There are examples for [lending](docs/examples/lending.md) and a [crypto exchange](docs/examples/exchange.md) too.

## License

[MIT](LICENSE) © 2026 Pandabase.
