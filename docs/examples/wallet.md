# Wallet

This example follows Alice and Bob as they top up a wallet, send money and withdraw to a bank account. You’ll see how Astrum prevents overspending and keeps withdrawal funds reserved while a bank transfer is pending.

Before you start, follow the [quick start](../../README.md#quick-start) to run Astrum and set `ASTRUM_KEY`. Run the blocks below in order, in the same Bash or Zsh session, with `jq` and curl 7.82 or newer. Amounts are USD cents.

These requests record the accounting for the example. Your application handles the actual payments and transfers through its banking or payment provider.

## Set up the accounts

```sh
URL=http://localhost:8080
AUTH="Authorization: Bearer $ASTRUM_KEY"

LEDGER=$(curl -sS $URL/v1/ledgers -H "$AUTH" --json '{"name":"Wallet"}' | jq -r .id)

account() {
  curl -sS $URL/v1/accounts -H "$AUTH" --json @- <<EOF | jq -r .id
{"ledger_id": "$LEDGER", "code": "$1", "currency": "USD", "normal_side": "$2"}
EOF
}

BANK=$(account bank debit)            # pooled cash backing every wallet
ALICE=$(account wallet:alice credit)  # owed to Alice
BOB=$(account wallet:bob credit)      # owed to Bob
```

## 1. Alice tops up $50

```sh
curl -sS $URL/v1/transactions -H "$AUTH" -H "Idempotency-Key: topup-1-$LEDGER" --json @- <<EOF
{"description": "Top-up", "entries": [
  {"account_id": "$BANK",  "side": "debit",  "amount": "5000"},
  {"account_id": "$ALICE", "side": "credit", "amount": "5000"}]}
EOF
```

## 2. Alice sends Bob $20

```sh
curl -sS $URL/v1/transactions -H "$AUTH" -H "Idempotency-Key: p2p-1-$LEDGER" --json @- <<EOF
{"description": "Dinner", "entries": [
  {"account_id": "$ALICE", "side": "debit",  "amount": "2000"},
  {"account_id": "$BOB",   "side": "credit", "amount": "2000"}]}
EOF
```

Alice has $30 left, so sending $40 more fails:

```sh
curl -sS $URL/v1/transactions -H "$AUTH" -H "Idempotency-Key: p2p-2-$LEDGER" --json @- <<EOF | jq -r .code
{"entries": [
  {"account_id": "$ALICE", "side": "debit",  "amount": "4000"},
  {"account_id": "$BOB",   "side": "credit", "amount": "4000"}]}
EOF
```

```text
insufficient_funds
```

## 3. Bob withdraws $15

Create a `pending` transaction while your application waits for the bank transfer to finish. This reserves Bob’s $15 so he can’t spend it again.

```sh
WITHDRAWAL=$(curl -sS $URL/v1/transactions -H "$AUTH" -H "Idempotency-Key: withdraw-1-$LEDGER" --json @- <<EOF | jq -r .id
{"status": "pending", "description": "Withdrawal", "entries": [
  {"account_id": "$BOB",  "side": "debit",  "amount": "1500"},
  {"account_id": "$BANK", "side": "credit", "amount": "1500"}]}
EOF
)

curl -sS $URL/v1/accounts/$BOB -H "$AUTH" | jq -c '{posted: .balances.posted.amount, available: .balances.available.amount}'
```

Bob's posted balance is unchanged, but the money is no longer available to spend:

```text
{"posted":"2000","available":"500"}
```

When the bank confirms the transfer, post the transaction. If the transfer fails, call `POST /v1/transactions/$WITHDRAWAL/archive` instead to release the reserved funds.

```sh
curl -sS -X POST $URL/v1/transactions/$WITHDRAWAL/post -H "$AUTH" | jq -r .status
```

```text
posted
```

## 4. Keep a $5 minimum

Suppose your wallet requires users to keep at least $5 available. Add a balance lock to Alice’s entry so Astrum checks that rule before posting the transaction. Sending $26 would leave her with only $4, so this request fails:

```sh
curl -sS $URL/v1/transactions -H "$AUTH" -H "Idempotency-Key: p2p-3-$LEDGER" --json @- <<EOF | jq -r .code
{"entries": [
  {"account_id": "$ALICE", "side": "debit", "amount": "2600", "available_balance_amount": {"gte": "500"}},
  {"account_id": "$BOB",   "side": "credit", "amount": "2600"}]}
EOF
```

```text
balance_lock_failed
```

Sending $25 leaves exactly $5 and succeeds:

```sh
curl -sS $URL/v1/transactions -H "$AUTH" -H "Idempotency-Key: p2p-4-$LEDGER" --json @- <<EOF | jq -r .status
{"entries": [
  {"account_id": "$ALICE", "side": "debit", "amount": "2500", "available_balance_amount": {"gte": "500"}},
  {"account_id": "$BOB",   "side": "credit", "amount": "2500"}]}
EOF
```

```text
posted
```

## Check the final balances

```sh
curl -sS "$URL/v1/accounts?ledger_id=$LEDGER" -H "$AUTH" | jq -r '.data | reverse[] | "\(.code) \(.balances.posted.amount)"'
```

```text
bank 3500
wallet:alice 500
wallet:bob 3000
```
