# Ride-hailing wallet and driver payouts

Imagine a ride-hailing app with a prepaid rider wallet. The rider has $100, the trip estimate is $35, the final fare is $28, and the rider adds a $5 tip. The driver gets 80% of the fare and the entire tip.

This hypothetical design uses prepaid funds. For external card payments, your payment service must separately manage the card authorization and capture.

Before you start, follow the [quick start](../../README.md#quick-start) and set `ASTRUM_KEY` to a write or admin key. Run the blocks in order in the same Bash or Zsh session, with `jq` and curl 7.82 or newer. Amounts are USD cents.

Astrum records the accounting. Your application talks to payment providers, keeps the business workflow and reconciles external transfers. The numbers below are illustrative; fees, tax and recognition rules depend on your product.

## 1. Set up and fund the wallet

```sh
URL=http://localhost:8080
AUTH="Authorization: Bearer $ASTRUM_KEY"

get() {
  curl -sS "$URL/v1$1" -H "$AUTH"
}

post() {
  curl -sS "$URL/v1$1" -H "$AUTH" -H "Idempotency-Key: ${2:-}" --json @-
}

LEDGER=$(post "/ledgers" <<<'{"name":"Ride hailing"}' | jq -r .id)

account() {
  post "/accounts" <<EOF | jq -r .id
{"ledger_id":"$LEDGER","code":"$1","currency":"USD","normal_side":"$2"}
EOF
}

BANK=$(account bank debit)
RIDER=$(account rider:alice credit)
FARES=$(account unallocated_fares credit)
DRIVER=$(account driver:sam credit)
REVENUE=$(account platform_revenue credit)

post "/transactions" "rider-topup-$LEDGER" <<EOF
{"description":"Confirmed wallet top-up","entries":[
  {"account_id":"$BANK","side":"debit","amount":"10000"},
  {"account_id":"$RIDER","side":"credit","amount":"10000"}]}
EOF
```

`bank` is the cash backing the wallet. The other accounts track money owed to the rider, unallocated fares, money owed to the driver and platform revenue.

## 2. Reserve the estimated fare

A $35 hold leaves $65 available for other spending while the trip is running. Choose a realistic expiry when integrating this flow; the fixed future date keeps the example easy to run.

```sh
HOLD=$(post "/holds" "trip-42-reserve-$LEDGER" <<EOF | jq -r .id
{"account_id":"$RIDER","amount":"3500","description":"Trip 42 estimate","expires_at":"2030-01-01T00:00:00Z"}
EOF
)

get "/accounts/$RIDER" | jq '.balances.available.amount'
```

```text
"6500"
```

## 3. Capture the final $28 fare

Capture creates a transaction from the rider wallet to `unallocated_fares` and releases the unused $7. It resolves the whole hold, so the unused portion does not stay reserved.

```sh
post "/holds/$HOLD/capture" "trip-42-capture-$LEDGER" <<EOF
{"destination_account_id":"$FARES","amount":"2800","description":"Trip 42 fare","metadata":{"trip":"42"}}
EOF
```

Allocate the fare after capture: $22.40 to the driver and $5.60 to the platform.

```sh
post "/transactions" "trip-42-split-$LEDGER" <<EOF
{"external_id":"trip-42:allocation","metadata":{"trip":"42"},"entries":[
  {"account_id":"$FARES","side":"debit","amount":"2800"},
  {"account_id":"$DRIVER","side":"credit","amount":"2240"},
  {"account_id":"$REVENUE","side":"credit","amount":"560"}]}
EOF
```

Capture and allocation are separate operations. If allocation fails, the fare remains in `unallocated_fares`; retry the allocation using the same key. Persist the workflow per trip so one trip’s allocation cannot consume another trip’s funds by mistake.

## 4. Add a $5 tip

The tip goes entirely to the driver. The rider now has $67 left, and the driver is owed $27.40.

```sh
post "/transactions" "trip-42-tip-$LEDGER" <<EOF
{"external_id":"trip-42:tip","metadata":{"trip":"42"},"entries":[
  {"account_id":"$RIDER","side":"debit","amount":"500"},
  {"account_id":"$DRIVER","side":"credit","amount":"500"}]}
EOF
```

## 5. Handle a failed driver payout

Reserve the $27.40 before asking your payout provider to transfer it. This stops another payout from spending the same available funds.

```sh
PAYOUT=$(post "/transactions" "driver-payout-attempt-1-$LEDGER" <<EOF | jq -r .id
{"status":"pending","description":"Driver payout attempt 1","entries":[
  {"account_id":"$DRIVER","side":"debit","amount":"2740"},
  {"account_id":"$BANK","side":"credit","amount":"2740"}]}
EOF
)
```

Suppose the provider confirms that the transfer failed. Archive the reservation:

```sh
post "/transactions/$PAYOUT/archive" <<<'{}'
```

Create a new reservation for the second attempt, with a new operation key. An HTTP retry of the first attempt would keep its original key instead.

```sh
RETRY=$(post "/transactions" "driver-payout-attempt-2-$LEDGER" <<EOF | jq -r .id
{"status":"pending","description":"Driver payout attempt 2","entries":[
  {"account_id":"$DRIVER","side":"debit","amount":"2740"},
  {"account_id":"$BANK","side":"credit","amount":"2740"}]}
EOF
)
```

After the second transfer is confirmed, post it:

```sh
post "/transactions/$RETRY/post" <<<'{}'
```

A timeout is not a confirmed failure. Keep the reservation and query the provider before starting another transfer; otherwise both payouts could succeed.

## Check the result

```sh
get "/accounts?ledger_id=$LEDGER" | jq -r '.data | reverse[] | "\(.code) \(.balances.posted.amount)"'
```

```text
bank 7260
rider:alice 6700
unallocated_fares 0
driver:sam 0
platform_revenue 560
```

The $72.60 at the bank backs the rider’s $67 plus the platform’s $5.60 revenue.

## Other trip outcomes

| Outcome | What to do |
| --- | --- |
| Rider cancels before capture | Void the hold. Record any cancellation fee as a separate approved transaction. |
| Fare exceeds the estimate | Obtain approval and enough additional available funds before charging. A capture cannot exceed its hold. |
| Tip callback arrives twice | Use the persisted tip operation key, not a new random key per callback. |
| Driver disputes their share | Post a correcting transaction; keep the original fare and allocation history. |
| Large nightly payout run | Create independent pending payout transactions through bulk requests, then track each provider transfer separately. |
