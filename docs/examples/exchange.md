# Crypto exchange

Alice deposits dollars and buys ether from the exchange’s inventory. This example records the dollar payment, the ether purchase and the trading fee in one transaction. Either all three are recorded, or none are.

Before you start, follow the [quick start](../../README.md#quick-start) to run Astrum and set `ASTRUM_KEY`. Run the blocks below in order, in the same Bash or Zsh session, with `jq` and curl 7.82 or newer. Amounts are minor units: USD cents and ETH wei.

These requests record the accounting for the example. Your application handles the actual payments and transfers through its banking or payment provider.

## Set up the accounts

First, register ETH with an exponent of 18 so amounts are expressed in wei. You only need to do this once: the exponent can’t change, and registering ETH again returns `currency_exists`. If it’s already registered, continue with the ledger setup.

```sh
URL=http://localhost:8080
AUTH="Authorization: Bearer $ASTRUM_KEY"

curl -sS $URL/v1/currencies -H "$AUTH" --json '{"code":"ETH","exponent":18}'

LEDGER=$(curl -sS $URL/v1/ledgers -H "$AUTH" --json '{"name":"Exchange"}' | jq -r .id)

account() {
  curl -sS $URL/v1/accounts -H "$AUTH" --json @- <<EOF | jq -r .id
{"ledger_id": "$LEDGER", "code": "$1", "currency": "$2", "normal_side": "$3"}
EOF
}

BANK=$(account bank USD debit)                # customer dollars at the bank
HOT_WALLET=$(account hot_wallet ETH debit)    # ether held on chain
ALICE_USD=$(account alice:usd USD credit)     # Alice's dollars
ALICE_ETH=$(account alice:eth ETH credit)     # Alice's ether
HOUSE_USD=$(account house:usd USD credit)     # the exchange's own dollars
HOUSE_ETH=$(account house:eth ETH credit)     # the exchange's own ether
FEES=$(account trading_fees USD credit)       # fee revenue
```

## 1. Funding

The exchange holds 10 ETH, and Alice deposits $3,000.

```sh
curl -sS $URL/v1/transactions -H "$AUTH" -H "Idempotency-Key: house-eth-$LEDGER" --json @- <<EOF
{"description": "House inventory", "entries": [
  {"account_id": "$HOT_WALLET", "side": "debit",  "amount": "10000000000000000000"},
  {"account_id": "$HOUSE_ETH",  "side": "credit", "amount": "10000000000000000000"}]}
EOF

curl -sS $URL/v1/transactions -H "$AUTH" -H "Idempotency-Key: deposit-alice-$LEDGER" --json @- <<EOF
{"description": "Deposit", "entries": [
  {"account_id": "$BANK",      "side": "debit",  "amount": "300000"},
  {"account_id": "$ALICE_USD", "side": "credit", "amount": "300000"}]}
EOF
```

## 2. Alice buys 0.5 ETH for $1,500 plus a $15 fee

The USD entries balance against each other, and so do the ETH entries. This lets us record the whole trade, including the fee, in a single transaction.

```sh
curl -sS $URL/v1/transactions -H "$AUTH" -H "Idempotency-Key: trade-1-$LEDGER" --json @- <<EOF | jq -r .status
{"description": "Buy 0.5 ETH", "entries": [
  {"account_id": "$ALICE_USD", "side": "debit",  "amount": "151500"},
  {"account_id": "$HOUSE_USD", "side": "credit", "amount": "150000"},
  {"account_id": "$FEES",      "side": "credit", "amount": "1500"},
  {"account_id": "$HOUSE_ETH", "side": "debit",  "amount": "500000000000000000"},
  {"account_id": "$ALICE_ETH", "side": "credit", "amount": "500000000000000000"}]}
EOF
```

```text
posted
```

A USD debit can’t balance an ETH credit. If we try that instead, Astrum rejects the transaction:

```sh
curl -sS $URL/v1/transactions -H "$AUTH" -H "Idempotency-Key: trade-2-$LEDGER" --json @- <<EOF | jq -r .code
{"entries": [
  {"account_id": "$ALICE_USD", "side": "debit",  "amount": "150000"},
  {"account_id": "$ALICE_ETH", "side": "credit", "amount": "150000"}]}
EOF
```

```text
unbalanced_transaction
```

## Check the final balances

```sh
curl -sS "$URL/v1/accounts?ledger_id=$LEDGER" -H "$AUTH" | jq -r '.data | reverse[] | "\(.code) \(.balances.posted.amount) \(.currency)"'
```

```text
bank 300000 USD
hot_wallet 10000000000000000000 ETH
alice:usd 148500 USD
alice:eth 500000000000000000 ETH
house:usd 150000 USD
house:eth 9500000000000000000 ETH
trading_fees 1500 USD
```
