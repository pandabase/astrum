<p align="center">
  <img src="assets/astrum-logo.png" alt="Astrum" width="420">
</p>

<p align="center">
  <strong>A financial kernel with a double-entry core.</strong><br>
</p>

Astrum keeps track of money: who owns it, where it moves, and what’s available to spend. It is a small, trusted kernel built with Go and PostgreSQL around a double-entry ledger that never lets money appear, vanish or be rewritten, with a JSON API for accounts, transactions, holds and settlements. Events and webhooks let your application follow along as things change.

Start with the [API reference](docs/api.md), or follow an example to see how the pieces fit together:

- [Wallet](docs/examples/wallet.md): top up, send money and withdraw.
- [Marketplace](docs/examples/marketplace.md): split payments, track fees and settle vendor balances.
- [Card authorization](docs/examples/card-authorization.md): reserve funds, capture a payment and release the rest.
- [Lending](docs/examples/lending.md): record a loan, schedule interest and apply repayments.
- [Crypto exchange](docs/examples/exchange.md): record both sides of a trade and its fee together.

## Quick start

You’ll need Go 1.27.1 and a running PostgreSQL database. Create a database named `astrum`, then set its connection URL:

```sh
export DATABASE_URL='postgres://postgres:postgres@localhost:5432/astrum?sslmode=disable'
```

Generate a seal key once with `openssl rand -hex 32`. Save it somewhere outside the database and reuse it whenever you start Astrum. This key signs the ledger’s tamper-evident history; startup checks it against the existing seal chain.

```sh
export LEDGER_SEAL_KEY='<your saved seal key>'
```

Create your first admin API key, then start the server:

```sh
go run ./cmd/astrum keys create -name ops
go run ./cmd/astrum
```

The first command prints the API key only once, so save it before continuing. In another terminal, use that key to create a ledger:

```sh
export ASTRUM_KEY='sk_...'
curl http://localhost:8080/v1/ledgers \
  -H "Authorization: Bearer $ASTRUM_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Payments"}'
```

Astrum runs database migrations on startup. It reads configuration from the environment, so export variables in your shell; `.env` files aren’t loaded automatically.

## Web interface

Build the interface once, then start the server with `WEB_DIR` pointing at it and sign in at `http://localhost:8080` with an API key:

```sh
pnpm -C web install && pnpm -C web build
WEB_DIR=web/build/client go run ./cmd/astrum
```

See [web/README.md](web/README.md) to work on the interface.

## Configuration

Most settings can stay at their defaults while you’re getting started.

| Variable                     | Default  | What it controls                                                                          |
| ---------------------------- | -------- | ----------------------------------------------------------------------------------------- |
| `DATABASE_URL`               | Required | PostgreSQL connection URL.                                                                |
| `LEDGER_SEAL_KEY`            | Required | Seal key of at least 32 bytes; reuse the same key across restarts.                        |
| `HTTP_ADDR`                  | `:8080`  | Address the API listens on.                                                               |
| `LOG_LEVEL`                  | `info`   | Logging level.                                                                            |
| `LOG_FORMAT`                 | `text`   | Log format: `text`, `json` or `logfmt`.                                                   |
| `DB_MAX_CONNS`               | `32`     | Maximum database connections.                                                             |
| `LEDGER_WORKERS`             | `8`      | Ledger workers; must be below `DB_MAX_CONNS`.                                             |
| `LEDGER_MAX_BATCH`           | `256`    | Maximum ledger worker batch size.                                                         |
| `DB_ALLOW_UNSAFE_DURABILITY` | `false`  | Allow database settings that could lose acknowledged commits. For local development only. |
| `EVENT_RETENTION`            | `720h`   | How long to keep delivered events and their webhook logs.                                 |
| `WEBHOOK_ALLOW_INSECURE`     | `false`  | Allow HTTP and private-network webhook URLs for local development.                        |
| `WEB_DIR`                    | Unset    | Built web interface to serve next to the API, such as `web/build/client`.                 |

## Benchmarking

`astrum-bench` load-tests a running server through its API. Each run creates its own `bench-…` ledger and accounts, so your data is untouched.

```sh
export ASTRUM_KEY='sk_...'   # a write or admin key
go run ./cmd/astrum-bench -scenario transfer -duration 30s -concurrency 32 -verify
```

Scenarios are `transfer`, `batch` (`-batch` transfers per request), `pending` (create, then post), `hold` (hold, then capture) and `read`. Use `-accounts 2` to make every transfer contend for the same rows, `-rate` for a fixed request rate with latency measured from each request's scheduled time, and `-json` for machine-readable output. It exits non-zero when any operation fails or the integrity check does. Run `-h` for every flag.

## Running tests

Set a test database URL to include the PostgreSQL integration tests:

```sh
ASTRUM_TEST_DATABASE_URL='postgres://...' go test ./...
```

Without `ASTRUM_TEST_DATABASE_URL`, the integration tests are skipped.

Tests that use a package only through its public API live in `tests/`, one folder per package. Tests of a package's unexported internals, and the server test in `cmd/astrum`, stay next to the code they test because Go only allows that from inside the package. The web interface's tests are in `web/tests/`.

## License

[MIT](LICENSE) © 2026 Pandabase.
