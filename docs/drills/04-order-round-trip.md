# Drill 04 — Order round trip

**Teaches:** the relationship between `ExecType (150)` and `OrdStatus (39)`, and
why the cumulative fields are the ones you build state from.

## Run it

The order-entry client sends a limit order every ten seconds. Watch one through
its whole life:

```bash
make up
docker compose logs -f exchange oe-client

# Find a working order
curl -s localhost:8081/admin/orders | jq '.[0]'

# Fill 40 of 100 at 25.50
curl -XPOST 'localhost:8081/admin/partial?clordid=CL000001&qty=40&px=25.50'

# Fill the remaining 60 at a different price
curl -XPOST 'localhost:8081/admin/fill?clordid=CL000001&px=25.60'
```

Nothing fills on its own. The venue acknowledges an order and then waits — see
[the design note](#why-nothing-fills-by-itself) at the bottom.

## Wire trace

The order out:

```
OECLIENT->FIXLABEX  -->  8=FIX.4.4|9=143|35=D|34=2|49=OECLIENT|56=FIXLABEX|
                         1=ACC1|11=CL000001|38=100|40=2|44=25.50|54=1|55=PETR4|
                         59=0|60=…|10=206|
```

Acknowledgement — `150=0`, `39=0`, nothing traded:

```
FIXLABEX->OECLIENT  -->  8=FIX.4.4|35=8|34=2|49=FIXLABEX|56=OECLIENT|
                         1=ACC1|6=0.00|11=CL000001|14=0|17=EXE000001|37=ORD000001|
                         38=100|39=0|44=25.50|54=1|55=PETR4|150=0|151=100|10=180|
                                          ▲     ▲                    ▲     ▲
                                     CumQty=0  OrdStatus=New   ExecType=New  LeavesQty=100
```

Partial fill — `150=F`, `39=1`:

```
FIXLABEX->OECLIENT  -->  8=FIX.4.4|35=8|34=3|…|6=25.50|11=CL000001|14=40|
                         17=EXE000002|31=25.50|32=40|37=ORD000001|38=100|39=1|
                         150=F|151=60|10=184|
                                ▲   ▲   ▲          ▲     ▲     ▲
                            AvgPx  Cum LastPx  LastQty  Status  Leaves
                                   =40  =25.50    =40    =Partial  =60
```

Final fill — `150=F` again, but `39=2`:

```
FIXLABEX->OECLIENT  -->  8=FIX.4.4|35=8|34=4|…|6=25.56|11=CL000001|14=100|
                         17=EXE000003|31=25.60|32=60|38=100|39=2|150=F|151=0|10=192|
                                ▲                                ▲
                          AvgPx=25.56                     OrdStatus=Filled
                    (40×25.50 + 60×25.60)/100
```

## What to notice

**`ExecType` and `OrdStatus` are not the same field twice.** `150` says what
just happened; `39` says where the order now stands. A partial fill is
`150=F` with `39=1`. The final fill of the same order is *also* `150=F`, but
`39=2`. Reading only `150` you cannot tell the order is done; reading only `39`
you cannot tell what caused the change. Both fills above carry `150=F` — the
difference is entirely in `39` and `151`.

**Build state from `14` and `151`, not from `32`.** `CumQty (14)` and
`LeavesQty (151)` are running totals; `LastQty (32)` is the increment. Assigning
a total is idempotent, adding an increment is not — and drill 07 replays
messages you have already seen. A client that accumulates `LastQty` will
double-count the moment it recovers from a gap. This is the single most
expensive mistake available in FIX, and the fix is to never write the additive
version in the first place.

**`AvgPx (6)` is weighted across everything executed so far**, not the last
price. 25.56, not 25.60. A client reconciling on the last fill price will
diverge from the venue on any order that fills at more than one price.

**`11=CL000001` versus `37=ORD000001`.** `ClOrdID` is yours and must be unique;
`OrderID` is the venue's. Cancels and replaces reference your ID, reconciliation
generally happens on theirs.

**`17=ExecID` is new on every report.** Drill 05 shows why that matters.

## Why nothing fills by itself

The venue has no matching engine. It acknowledges an order and then does exactly
what the admin API tells it to.

That is a deliberate limit. A matching engine would make fills arrive on their
own schedule, which would make every wire trace in these docs different on every
run, and CI could not assert on them. It would also be a second large thing to
maintain in a repository about session management. Real order books are a
different lab.

## Versus real B3

- B3 matches continuously; fills arrive unsolicited and out of your control.
- B3 rejects orders for reasons this lab does not model — price bands, risk
  limits, self-trade prevention, instrument state.
- `59=TimeInForce` has more values, and they change what the venue does with a
  residual quantity.
- Cancels and replaces (`35=F`, `35=G`) are not modeled here at all.

## The test

[`internal/drills/drill04_order_round_trip_test.go`](../../internal/drills/drill04_order_round_trip_test.go)
walks the same order through ack, partial and fill, and checks the weighted
average against a hand-computed 25.56.
