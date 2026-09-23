# Card authorization

Fuel pumps and hotels often reserve more money than a customer eventually spends. This example follows Alice’s prepaid card through an authorization, a partial capture and a cancelled hold, then records settlement with the card network.

Before you start, follow the [quick start](../../README.md#quick-start) to run Astrum and set `ASTRUM_KEY`. Run the blocks below in order, in the same Bash or Zsh session, with `jq` and curl 7.82 or newer. Amounts are USD cents.

These requests record the accounting for the example. Your application handles the actual payments and transfers through its banking or payment provider.

## Set up the accounts

```sh
URL=http://localhost:8080
AUTH="Authorization: Bearer $ASTRUM_KEY"

LEDGER=$(curl -sS $URL/v1/ledgers -H "$AUTH" --json '{"name":"Cards"}' | jq -r .id)

account() {
  curl -sS $URL/v1/accounts -H "$AUTH" --json @- <<EOF | jq -r .id
{"ledger_id": "$LEDGER", "code": "$1", "currency": "USD", "normal_side": "$2"}
EOF
}

BANK=$(account bank debit)                       # cash backing card balances
CARD=$(account card:alice credit)                # Alice's card balance
NETWORK=$(account network_settlement credit)     # owed to the card network

balances() {
  curl -sS $URL/v1/accounts/$CARD -H "$AUTH" | jq -c '{posted: .balances.posted.amount, available: .balances.available.amount}'
}
```

## 1. Alice loads $200

```sh
curl -sS $URL/v1/transactions -H "$AUTH" -H "Idempotency-Key: load-1-$LEDGER" --json @- <<EOF
{"description": "Card load", "entries": [
  {"account_id": "$BANK", "side": "debit",  "amount": "20000"},
  {"account_id": "$CARD", "side": "credit", "amount": "20000"}]}
EOF
```

## 2. Fuel pump authorizes $100

The hold sets aside $100 from Alice’s available balance without changing her posted balance. If it isn’t captured, the money becomes available again at `expires_at`.

```sh
FUEL=$(curl -sS $URL/v1/holds -H "$AUTH" -H "Idempotency-Key: auth-fuel-$LEDGER" --json @- <<EOF | jq -r .id
{"account_id": "$CARD", "amount": "10000", "description": "Fuel pre-auth", "expires_at": "2030-01-01T00:00:00Z"}
EOF
)
balances
```

```text
{"posted":"20000","available":"10000"}
```

## 3. Pump captures $62.40

The capture posts what was spent to `network_settlement` and releases the other $37.60.

```sh
curl -sS $URL/v1/holds/$FUEL/capture -H "$AUTH" -H "Idempotency-Key: capture-fuel-$LEDGER" --json @- <<EOF | jq -c '{status, captured_amount}'
{"destination_account_id": "$NETWORK", "amount": "6240"}
EOF
balances
```

```text
{"status":"captured","captured_amount":"6240"}
{"posted":"13760","available":"13760"}
```

## 4. Release a $50 hotel hold

The hotel reserves $50 for incidentals. Alice checks out without any extra charges, so the hotel voids the hold and the full amount becomes available again.

```sh
HOTEL=$(curl -sS $URL/v1/holds -H "$AUTH" -H "Idempotency-Key: auth-hotel-$LEDGER" --json @- <<EOF | jq -r .id
{"account_id": "$CARD", "amount": "5000", "description": "Hotel incidentals", "expires_at": "2030-01-01T00:00:00Z"}
EOF
)
balances

curl -sS -X POST $URL/v1/holds/$HOTEL/void -H "$AUTH" | jq -r .status
balances
```

```text
{"posted":"13760","available":"8760"}
voided
{"posted":"13760","available":"13760"}
```

## 5. Pay the network

Once the network is paid, record the payout by settling `network_settlement` into `bank`. The settlement covers everything captured since the previous settlement.

```sh
curl -sS $URL/v1/settlements -H "$AUTH" -H "Idempotency-Key: network-$LEDGER" --json @- <<EOF | jq -r .amount
{"settled_account_id": "$NETWORK", "contra_account_id": "$BANK", "description": "Network settlement"}
EOF
```

```text
6240
```

## Check the final balances

```sh
curl -sS "$URL/v1/accounts?ledger_id=$LEDGER" -H "$AUTH" | jq -r '.data | reverse[] | "\(.code) \(.balances.posted.amount)"'
```

```text
bank 13760
card:alice 13760
network_settlement 0
```
