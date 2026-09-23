# Multi-seller commerce

Imagine an Amazon-style storefront: one checkout, two sellers, a partial return and separate payouts. This is a hypothetical implementation using Astrum, not a description of Amazon’s systems.

A customer pays $300: $200 for seller Acme’s item and $100 for seller Birch’s. The platform keeps 10%. Before payout, the customer gets a $40 partial refund on Birch’s item.

Before you start, follow the [quick start](../../README.md#quick-start) and set `ASTRUM_KEY` to a write or admin key. Run the blocks in order in the same Bash or Zsh session, with `jq` and curl 7.82 or newer. Amounts are USD cents.

Astrum records the accounting. Your application talks to payment providers, keeps the business workflow and reconciles external transfers. The numbers below are illustrative; fees, tax and recognition rules depend on your product.

## 1. Set up the accounts

Keep customer funds waiting for allocation separate from seller payables and earned commission. In this example, the application releases each seller’s share after delivery.

| Account | Normal side | Tracks |
| --- | --- | --- |
| `processor` | debit | Money collected by the processor but not deposited yet. |
| `bank` | debit | Confirmed cash at the bank. |
| `orders` | credit | Customer payments waiting for allocation. |
| `seller:acme`, `seller:birch` | credit | Amounts owed to each seller. |
| `refunds` | credit | Approved refunds waiting to be sent. |
| `commission` | credit | Platform revenue. |
| `processing_fees` | debit | Processor charges. |

`get` reads a resource. `post` sends the JSON below it; its optional second argument is the operation’s idempotency key.

```sh
URL=http://localhost:8080
AUTH="Authorization: Bearer $ASTRUM_KEY"

get() {
  curl -sS "$URL/v1$1" -H "$AUTH"
}

post() {
  curl -sS "$URL/v1$1" -H "$AUTH" -H "Idempotency-Key: ${2:-}" --json @-
}

LEDGER=$(post "/ledgers" <<<'{"name":"Commerce"}' | jq -r .id)

account() {
  post "/accounts" <<EOF | jq -r .id
{"ledger_id":"$LEDGER","code":"$1","currency":"USD","normal_side":"$2"}
EOF
}

PROCESSOR=$(account processor debit)
BANK=$(account bank debit)
ORDERS=$(account orders credit)
ACME=$(account seller:acme credit)
BIRCH=$(account seller:birch credit)
REFUNDS=$(account refunds credit)
COMMISSION=$(account commission credit)
PROCESSING_FEES=$(account processing_fees debit)
```

## 2. Record the confirmed $300 payment

The payment service calls this after the processor confirms capture. The order ID connects this journal entry to the checkout; use a different reference for each later stage.

```sh
post "/transactions" "payment-1001-$LEDGER" <<EOF
{"external_id":"order-1001:payment","metadata":{"order":"1001"},"entries":[
  {"account_id":"$PROCESSOR","side":"debit","amount":"30000"},
  {"account_id":"$ORDERS","side":"credit","amount":"30000"}]}
EOF
```

A card authorization alone is not a captured payment. Keep external authorization state in your payment service; an Astrum hold reserves an existing Astrum balance, not a customer’s external card limit.

## 3. Allocate each delivery

Acme gets $180 and Birch gets $90. The remaining $30 becomes platform commission. These are separate transactions so each shipment can complete independently.

```sh
post "/transactions" "acme-delivery-1001-$LEDGER" <<EOF
{"external_id":"order-1001:acme-delivered","metadata":{"order":"1001","seller":"acme"},"entries":[
  {"account_id":"$ORDERS","side":"debit","amount":"20000"},
  {"account_id":"$ACME","side":"credit","amount":"18000"},
  {"account_id":"$COMMISSION","side":"credit","amount":"2000"}]}
EOF

post "/transactions" "birch-delivery-1001-$LEDGER" <<EOF
{"external_id":"order-1001:birch-delivered","metadata":{"order":"1001","seller":"birch"},"entries":[
  {"account_id":"$ORDERS","side":"debit","amount":"10000"},
  {"account_id":"$BIRCH","side":"credit","amount":"9000"},
  {"account_id":"$COMMISSION","side":"credit","amount":"1000"}]}
EOF
```

If a shipment is canceled before allocation, move its amount from `orders` to `refunds` instead. The application decides which transition is allowed; an account balance alone does not identify the order whose funds it contains.

## 4. Approve a $40 partial return

Reduce Birch’s payable by $36 and return $4 of commission. Put the $40 in `refunds` until the payment provider confirms the refund. Don’t reverse the whole $100 delivery transaction for a partial return.

```sh
post "/transactions" "refund-1001-approved-$LEDGER" <<EOF
{"external_id":"order-1001:refund-1","metadata":{"order":"1001","refund":"refund-1"},"entries":[
  {"account_id":"$BIRCH","side":"debit","amount":"3600"},
  {"account_id":"$COMMISSION","side":"debit","amount":"400"},
  {"account_id":"$REFUNDS","side":"credit","amount":"4000"}]}
EOF
```

Birch is now owed $54. The platform has $26 in commission and owes the customer $40.

## 5. Reconcile the processor deposit

The processor charges $9 and deposits $291. Record the fee, then record the confirmed deposit with a settlement. This scenario assumes the refund is funded separately from the bank after that deposit.

```sh
post "/transactions" "processor-fee-1001-$LEDGER" <<EOF
{"description":"Processor fee","entries":[
  {"account_id":"$PROCESSING_FEES","side":"debit","amount":"900"},
  {"account_id":"$PROCESSOR","side":"credit","amount":"900"}]}
EOF

post "/settlements" "processor-deposit-1001-$LEDGER" <<EOF
{"settled_account_id":"$PROCESSOR","contra_account_id":"$BANK","description":"Confirmed processor deposit"}
EOF
```

## 6. Record the refund and seller payouts

After the provider confirms the bank-funded $40 refund, clear its payable:

```sh
post "/transactions" "refund-1001-sent-$LEDGER" <<EOF
{"description":"Confirmed customer refund","metadata":{"order":"1001","refund":"refund-1"},"entries":[
  {"account_id":"$REFUNDS","side":"debit","amount":"4000"},
  {"account_id":"$BANK","side":"credit","amount":"4000"}]}
EOF
```

For Acme’s $180 payout, reserve funds while the transfer is in progress. Persist the returned transaction ID with the provider’s payout ID in your application.

```sh
PAYOUT=$(post "/transactions" "acme-payout-1001-$LEDGER" <<EOF | jq -r .id
{"status":"pending","description":"Acme payout","entries":[
  {"account_id":"$ACME","side":"debit","amount":"18000"},
  {"account_id":"$BANK","side":"credit","amount":"18000"}]}
EOF
)
```

When that transfer is confirmed, post it. If it definitively fails, archive it instead to release the reservation.

```sh
post "/transactions/$PAYOUT/post" <<<'{}'
```

Record Birch’s confirmed $54 payout with a settlement:

```sh
post "/settlements" "birch-payout-1001-$LEDGER" <<EOF
{"settled_account_id":"$BIRCH","contra_account_id":"$BANK","description":"Confirmed Birch payout"}
EOF
```

The two payout paths illustrate different workflows. A manual payout transaction does not mark the original seller entries as settled. Use one payout strategy consistently per flow; never let a separate settlement worker sweep accounts with payouts still in flight.

## Check the result

```sh
get "/accounts?ledger_id=$LEDGER" | jq -r '.data | reverse[] | "\(.code) \(.balances.posted.amount)"'
```

```text
processor 0
bank 1700
orders 0
seller:acme 0
seller:birch 0
refunds 0
commission 2600
processing_fees 900
```

The remaining $17 is $26 in commission minus $9 in processor fees.

## When this becomes a larger application

| Concern | Application behavior |
| --- | --- |
| Duplicate provider callbacks | Persist provider event IDs and reuse the same operation key. |
| Payment succeeds, ledger request times out | Retry the identical ledger request; reconcile against the processor before issuing another payment. |
| Return after the seller was paid | Track a seller receivable or an explicitly agreed reserve/overdraft policy. Don’t silently make a zero payable negative. |
| Partial shipments | Track allocations per order line so the same delivery cannot be allocated twice under different keys. |
| Tax, shipping and discounts | Add explicit accounts and balanced entries for the amounts your checkout calculated. |
| Multiple legal entities or currencies | Define separate ledger boundaries; model inter-entity movements explicitly. Transactions cannot cross ledgers. |
