<p align="center">
  <img src="assets/astrum-logo.png" alt="Astrum" width="420">
</p>

<p align="center"><strong>A financial kernel with a double-entry core.</strong></p>

Astrum tracks balances, transactions, holds and settlements through a JSON API. Built with Go and PostgreSQL, with a web interface for day-to-day operations.

[API reference](docs/api.md) · [Web development](web/README.md) · [Configuration](docs/api.md#server-configuration)

## Quick start

You’ll need Go 1.27.1 and a PostgreSQL database named `astrum`. Generate a seal key once with `openssl rand -hex 32`, save it outside the database, and reuse it on every start.

```sh
export DATABASE_URL='postgres://postgres:postgres@localhost:5432/astrum?sslmode=disable'
export LEDGER_SEAL_KEY='<your saved seal key>'

go run ./cmd/astrum keys create -name ops
go run ./cmd/astrum
```

Save the admin API key printed by the first command; it is shown only once. Migrations run automatically. Export your environment variables yourself—`.env` files aren’t loaded.

In another terminal:

```sh
export ASTRUM_KEY='sk_...'
curl http://localhost:8080/v1/ledgers \
  -H "Authorization: Bearer $ASTRUM_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Payments"}'
```

## Web interface

Stop the server, build the frontend, then restart with `WEB_DIR` set. Sign in at `http://localhost:8080` using your API key:

```sh
pnpm -C web install
pnpm -C web build
WEB_DIR=web/build/client go run ./cmd/astrum
```

## Examples

[Wallet](docs/examples/wallet.md) · [Marketplace](docs/examples/marketplace.md) · [Card authorization](docs/examples/card-authorization.md) · [Lending](docs/examples/lending.md) · [Crypto exchange](docs/examples/exchange.md)

## License

[MIT](LICENSE) © 2026 Pandabase.
