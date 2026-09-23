# Marketplace

In this example, a marketplace keeps a 10% commission on each sale and owes the rest to its vendor, Acme. A payment processor (PSP) collects the card payments and deposits them a few days later, minus its own fees.

Before you start, follow the [quick start](../../README.md#quick-start) to run Astrum and set `ASTRUM_KEY`. Run the blocks below in order, in the same Bash or Zsh session, with `jq` and curl 7.82 or newer. Amounts are USD cents.

These requests record the accounting for the example. Your application handles the actual payments and transfers through its banking or payment provider.

## Set up the accounts

Run this setup once. `get` reads a resource; `post` sends the JSON below it. The second argument to `post` is the idempotency key for that operation. Reuse it only when retrying the same request.

```sh
URL=http://localhost:8080
AUTH="Authorization: Bearer $ASTRUM_KEY"

get() {
  curl -sS "$URL/v1$1" -H "$AUTH"
}

post() {
  curl -sS "$URL/v1$1" -H "$AUTH" -H "Idempotency-Key: ${2:-}" --json @-
}

LEDGER=$(post "/ledgers" <<<'{"name":"Marketplace"}' | jq -r .id)

account() {
  post "/accounts" <<EOF | jq -r .id
{"ledger_id": "$LEDGER", "code": "$1", "currency": "USD", "normal_side": "$2"}
EOF
}

PSP=$(account psp_receivable debit)   # collected by the PSP, not yet deposited
BANK=$(account bank debit)            # our bank account
ACME=$(account vendor:acme credit)    # owed to the vendor
FEES=$(account fee_revenue credit)    # our commission
PSP_FEES=$(account psp_fees debit)    # what the PSP charges us
```

## 1. Customers pay

Record each sale as it happens, splitting the payment between Acme’s share and the marketplace’s commission. The `external_id` connects the transaction to an order and prevents that order from being recorded twice.

```sh
post "/transactions" "order-1001-$LEDGER" <<EOF
{"external_id": "order-1001", "description": "Order 1001", "entries": [
  {"account_id": "$PSP",  "side": "debit",  "amount": "10000"},
  {"account_id": "$ACME", "side": "credit", "amount": "9000"},
  {"account_id": "$FEES", "side": "credit", "amount": "1000"}]}
EOF

post "/transactions" "order-1002-$LEDGER" <<EOF
{"external_id": "order-1002", "description": "Order 1002", "entries": [
  {"account_id": "$PSP",  "side": "debit",  "amount": "5000"},
  {"account_id": "$ACME", "side": "credit", "amount": "4500"},
  {"account_id": "$FEES", "side": "credit", "amount": "500"}]}
EOF
```

## 2. Record the processor’s fee

The processor charges $3. Record it as an expense and reduce the amount we expect the processor to deposit.

```sh
post "/transactions" "psp-fees-$LEDGER" <<EOF
{"description": "PSP fees", "entries": [
  {"account_id": "$PSP_FEES", "side": "debit",  "amount": "300"},
  {"account_id": "$PSP",      "side": "credit", "amount": "300"}]}
EOF
```

## 3. PSP deposits

The processor deposits $147: $150 in sales minus its $3 fee. Record the deposit with a settlement, which moves the unsettled balance from `psp_receivable` to `bank` and marks the three entries as settled.

```sh
post "/settlements" "psp-deposit-$LEDGER" <<EOF | jq -c '{amount, entry_count}'
{"settled_account_id": "$PSP", "contra_account_id": "$BANK", "description": "PSP deposit"}
EOF
```

```text
{"amount":"14700","entry_count":3}
```

## 4. Pay the vendor

Before recording Acme’s payout, check which entries are still unsettled:

```sh
get "/entries?account_id=$ACME&settled=false" | jq -r '.data[] | "\(.side) \(.amount)"'
```

```text
credit 9000
credit 4500
```

Record the $135 payout by settling `vendor:acme` into `bank`. This covers both sales and clears the amount owed to Acme.

```sh
post "/settlements" "payout-acme-$LEDGER" <<EOF | jq -c '{amount, entry_count}'
{"settled_account_id": "$ACME", "contra_account_id": "$BANK", "description": "Acme payout"}
EOF
```

```text
{"amount":"13500","entry_count":2}
```

## Check the final balances

```sh
get "/accounts?ledger_id=$LEDGER" | jq -r '.data | reverse[] | "\(.code) \(.balances.posted.amount)"'
```

```text
psp_receivable 0
bank 1200
vendor:acme 0
fee_revenue 1500
psp_fees 300
```

The $12 left in the bank is the $15 commission less the $3 PSP fee.
