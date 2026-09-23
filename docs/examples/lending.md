# Lending

This example follows a $1,000 loan to Alice. We’ll record the payout, schedule an interest charge and split her first repayment between interest and principal.

Before you start, follow the [quick start](../../README.md#quick-start) to run Astrum and set `ASTRUM_KEY`. Run the blocks below in order, in the same Bash or Zsh session, with `jq` and curl 7.82 or newer. Amounts are USD cents.

These requests record the accounting for the example. Your application handles the actual payments and transfers through its banking or payment provider.

## Set up the accounts

```sh
URL=http://localhost:8080
AUTH="Authorization: Bearer $ASTRUM_KEY"

LEDGER=$(curl -sS $URL/v1/ledgers -H "$AUTH" --json '{"name":"Lending"}' | jq -r .id)

account() {
  curl -sS $URL/v1/accounts -H "$AUTH" --json @- <<EOF | jq -r .id
{"ledger_id": "$LEDGER", "code": "$1", "currency": "USD", "normal_side": "$2"}
EOF
}

BANK=$(account bank debit)                             # lender's cash
EQUITY=$(account equity credit)                        # capital put in
LOAN=$(account loan:alice debit)                       # principal Alice owes
INTEREST=$(account interest_receivable:alice debit)    # interest Alice owes
INCOME=$(account interest_income credit)               # interest earned
```

## 1. Capital and payout

Start by recording $5,000 of capital in the lender’s bank account. Then record the $1,000 loan payout: cash falls by $1,000, and the amount Alice owes rises by the same amount.

```sh
curl -sS $URL/v1/transactions -H "$AUTH" -H "Idempotency-Key: capital-$LEDGER" --json @- <<EOF
{"description": "Capital", "entries": [
  {"account_id": "$BANK",   "side": "debit",  "amount": "500000"},
  {"account_id": "$EQUITY", "side": "credit", "amount": "500000"}]}
EOF

curl -sS $URL/v1/transactions -H "$AUTH" -H "Idempotency-Key: disburse-$LEDGER" --json @- <<EOF
{"description": "Loan payout", "entries": [
  {"account_id": "$LOAN", "side": "debit",  "amount": "100000"},
  {"account_id": "$BANK", "side": "credit", "amount": "100000"}]}
EOF
```

## 2. Monthly interest

Use a scheduled transaction to record $10 of interest at `execute_at`. You’d normally choose the date the interest is due. Here we use the current time and wait for the worker to execute it.

```sh
ACCRUAL=$(curl -sS $URL/v1/scheduled_transactions -H "$AUTH" -H "Idempotency-Key: interest-2026-09-$LEDGER" --json @- <<EOF | jq -r .id
{"execute_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)", "description": "September interest", "entries": [
  {"account_id": "$INTEREST", "side": "debit",  "amount": "1000"},
  {"account_id": "$INCOME",   "side": "credit", "amount": "1000"}]}
EOF
)

until curl -sS $URL/v1/scheduled_transactions/$ACCRUAL -H "$AUTH" | jq -e '.status == "executed"' >/dev/null; do sleep 1; done
```

## 3. Alice repays $300

Of Alice’s $300 repayment, $10 clears the interest and the remaining $290 reduces the principal. We record both parts in one transaction.

```sh
curl -sS $URL/v1/transactions -H "$AUTH" -H "Idempotency-Key: repayment-1-$LEDGER" --json @- <<EOF
{"description": "Repayment", "entries": [
  {"account_id": "$BANK",     "side": "debit",  "amount": "30000"},
  {"account_id": "$INTEREST", "side": "credit", "amount": "1000"},
  {"account_id": "$LOAN",     "side": "credit", "amount": "29000"}]}
EOF
```

## 4. What Alice owes in total

Group Alice’s principal and interest accounts in a category to see her total outstanding balance.

```sh
OWES=$(curl -sS $URL/v1/account_categories -H "$AUTH" --json @- <<EOF | jq -r .id
{"ledger_id": "$LEDGER", "currency": "USD", "normal_side": "debit", "name": "Alice owes"}
EOF
)
curl -sS -X PUT $URL/v1/account_categories/$OWES/accounts/$LOAN -H "$AUTH" >/dev/null
curl -sS -X PUT $URL/v1/account_categories/$OWES/accounts/$INTEREST -H "$AUTH" >/dev/null

curl -sS $URL/v1/account_categories/$OWES -H "$AUTH" | jq -r .balances.posted.amount
```

```text
71000
```

## Check the final balances

```sh
curl -sS "$URL/v1/accounts?ledger_id=$LEDGER" -H "$AUTH" | jq -r '.data | reverse[] | "\(.code) \(.balances.posted.amount)"'
```

```text
bank 430000
equity 500000
loan:alice 71000
interest_receivable:alice 0
interest_income 1000
```
