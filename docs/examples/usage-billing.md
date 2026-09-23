# Usage-based SaaS billing

Imagine a cloud API that sells prepaid credits. A customer adds $500, consumes compute and storage, receives a usage correction, then refunds part of the unused credit.

Your metering service counts usage and calculates charges. Astrum records the resulting amounts; it does not calculate prices or enforce API quotas by itself.

Before you start, follow the [quick start](../../README.md#quick-start) and set `ASTRUM_KEY` to a write or admin key. Run the blocks in order in the same Bash or Zsh session, with `jq` and curl 7.82 or newer. Amounts are USD cents.

Astrum records the accounting. Your application talks to payment providers, keeps the business workflow and reconciles external transfers. The numbers below are illustrative; fees, tax and recognition rules depend on your product.

## 1. Set up and record the purchase

```sh
URL=http://localhost:8080
AUTH="Authorization: Bearer $ASTRUM_KEY"

get() {
  curl -sS "$URL/v1$1" -H "$AUTH"
}

post() {
  curl -sS "$URL/v1$1" -H "$AUTH" -H "Idempotency-Key: ${2:-}" --json @-
}

LEDGER=$(post "/ledgers" <<<'{"name":"Usage billing"}' | jq -r .id)

account() {
  post "/accounts" <<EOF | jq -r .id
{"ledger_id":"$LEDGER","code":"$1","currency":"USD","normal_side":"$2"}
EOF
}

BANK=$(account bank debit)
CREDITS=$(account customer:acme:credits credit)
REVENUE=$(account usage_revenue credit)

post "/transactions" "credits-purchase-$LEDGER" <<EOF
{"description":"Confirmed credit purchase","entries":[
  {"account_id":"$BANK","side":"debit","amount":"50000"},
  {"account_id":"$CREDITS","side":"credit","amount":"50000"}]}
EOF
```

The customer credit account is a liability: the service is still owed. This example moves credits to revenue as usage is delivered.

## 2. Watch for low credit

Set a monitor before usage begins. When available credit crosses below $250, an event can tell your application to notify the customer or offer a top-up.

```sh
MONITOR=$(post "/balance_monitors" <<EOF | jq -r .id
{"account_id":"$CREDITS","description":"Credit below 250 USD","alert_condition":{"field":"available_balance_amount","operator":"lt","value":"25000"}}
EOF
)
```

A monitor does not automatically pause service or charge a card. Your application handles that action and deduplicates trigger events.

## 3. Bill $120 of usage

Aggregate raw usage into a stable billing window, then record its charge. Persist the window ID and calculated amount before sending the request.

```sh
USAGE=$(post "/transactions" "usage-window-001-$LEDGER" <<EOF | jq -r .id
{"external_id":"acme:usage-window-001","metadata":{"customer":"acme","window":"001"},"entries":[
  {"account_id":"$CREDITS","side":"debit","amount":"12000"},
  {"account_id":"$REVENUE","side":"credit","amount":"12000"}]}
EOF
)
```

A repeated callback for this window uses the same key. A different key with the same `external_id` is also rejected within this ledger.

## 4. Correct a $20 overcharge

Suppose the meter counted some requests twice. Add a $20 adjustment referencing the original transaction. The customer’s credit rises from $380 to $400, while revenue falls from $120 to $100.

```sh
post "/transactions" "usage-correction-001-$LEDGER" <<EOF
{"external_id":"acme:usage-window-001:correction","metadata":{"original_transaction":"$USAGE"},"entries":[
  {"account_id":"$REVENUE","side":"debit","amount":"2000"},
  {"account_id":"$CREDITS","side":"credit","amount":"2000"}]}
EOF
```

Use a full reversal only if the entire original charge should be undone. A partial correction should be its own balanced transaction.

## 5. Refund $150 of unused credit

Reserve the refund while the payment provider processes it. Pending outflows reduce available credit immediately, so the same $150 cannot also pay for more usage.

```sh
REFUND=$(post "/transactions" "unused-credit-refund-$LEDGER" <<EOF | jq -r .id
{"status":"pending","description":"Unused credit refund","entries":[
  {"account_id":"$CREDITS","side":"debit","amount":"15000"},
  {"account_id":"$BANK","side":"credit","amount":"15000"}]}
EOF
)
```

After the provider confirms the refund:

```sh
post "/transactions/$REFUND/post" <<<'{}'
```

For a confirmed failure, archive instead. For an unknown outcome, keep the reservation and reconcile with the provider.

## 6. Post the next compute and storage charges together

The next window contains $40 of compute and $10 of storage. An atomic batch records both or neither, keeping the invoice window together.

```sh
post "/transactions/batch" "usage-window-002-$LEDGER" <<EOF
{"atomic":true,"transactions":[
  {"external_id":"acme:window-002:compute","description":"Compute usage","entries":[
    {"account_id":"$CREDITS","side":"debit","amount":"4000"},
    {"account_id":"$REVENUE","side":"credit","amount":"4000"}]},
  {"external_id":"acme:window-002:storage","description":"Storage usage","entries":[
    {"account_id":"$CREDITS","side":"debit","amount":"1000"},
    {"account_id":"$REVENUE","side":"credit","amount":"1000"}]}
]}
EOF
```

The customer now has $200. The monitor condition is true:

```sh
get "/balance_monitors/$MONITOR" | jq .triggered
```

```text
true
```

For independent customer charges, use a non-atomic batch or bulk request and inspect each result. One customer’s insufficient funds should not reject unrelated customers’ charges.

## Check the result

```sh
get "/accounts?ledger_id=$LEDGER" | jq -r '.data | reverse[] | "\(.code) \(.balances.posted.amount)"'
```

```text
bank 35000
customer:acme:credits 20000
usage_revenue 15000
```

Cash is $350 after the $150 refund. It backs $200 of unused customer credits and $150 of earned usage revenue.

## Taking this beyond the example

| Concern | Application behavior |
| --- | --- |
| Millions of usage events | Aggregate in the metering service; don’t post one transaction per API call unless that granularity is actually needed. |
| Long-running jobs | Reserve a budget with a hold or pending transaction, then capture/post the actual amount and release the rest. |
| Concurrent requests near zero credit | Coordinate service admission with reservations. Checking a balance and charging later leaves a race. |
| Promotional credits | Track them separately when they have different expiry or refund rules. |
| Monthly invoices | Keep invoice lines and tax calculations in billing; attach invoice IDs in metadata and use statements for ledger snapshots. |
| Automatic top-ups | Deduplicate monitor events and provider charge attempts. Credit the ledger only after the external payment is confirmed. |
