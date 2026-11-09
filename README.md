# graded-catalogue

_Built with AI assistance._

A marketplace catalogue where every unit is one of a kind.

A shop selling new goods has a SKU with a quantity behind it, and any two
units of that SKU are the same thing. Two refurbished iPhone 13 Pros in
256 GB are not: different casings, different battery health, different
sellers, different warranties, different prices. Quantity is always one.

Almost every awkward thing here follows from that sentence.

Go 1.23, Postgres.

```
internal/catalogue/offer.go   grades, states, and what a unit is
internal/catalogue/buybox.go  which offer to show, and the "from" price
internal/offers/reserve.go    holding a unit, and what to do when it is gone
internal/store/postgres/      the conditional update that stops a double sale
cmd/cataloguectl/
```

## There is no quantity column

The offers table has no quantity, on purpose. A quantity column on a table
where the number is always one invites `SET qty = qty - 1` and every bug that
comes with it. An offer has a state instead, and the states are the four a
unique object can be in.

    available ──reserve──▶ reserved ──sell──▶ sold
         ▲                    │
         └──release/expire────┘

`reserved` is the state most catalogues leave out. Without it, a unit in
somebody's basket is either available, in which case it gets sold twice, or
sold, in which case an abandoned basket destroys stock.

## The buy box is not the cheapest

Showing the lowest price ranks a fair unit at 380 euros above an excellent
one at 400. The customer who receives a scratched phone did not knowingly
save 20 euros; they were shown the cheapest thing and assumed it was the
best deal.

So each offer is scored, in cents, as a price with adjustments:

| | |
|---|---|
| Each grade below excellent | plus 6% of the price per step |
| Battery under 85%, where there is a battery | plus 4% |
| Seller with fewer than 10 ratings, or rated under 4.0 | plus 1500 cents, flat |
| Warranty | minus 100 cents a month, capped at 24 months |

Four decisions in that table are worth saying out loud.

**It is a price, not a score out of 100.** A number in cents can be explained
to a seller who asks why they lost the buy box, and a seller who cannot be
given that answer concludes the marketplace is rigged.

**6% a step is not derived from anything.** Somebody chose it. That is the
honest description of every weight in a ranking like this, and the reason it
is one named constant is so that changing it is one edit and one test. A case
asserts that a 100 euro saving still beats one grade, so the adjustment cannot
quietly become a sort by grade.

**The unrated seller penalty is flat, not a percentage.** A percentage would
punish a new seller far more on an expensive item, and the risk is the same
transaction either way. A new seller 50 euros cheaper still wins, and that is
asserted, because a penalty no new seller can overcome is a closed market.

**Battery is only counted where there is one.** A monitor reports zero, and
reading that as a dead battery penalises every item that never had one.

Ties break on the offer id. A buy box that moves between two page loads looks
broken to a customer and rigged to a seller.

## Only available offers are ever shown

A reserved offer is in somebody's basket and may come back. Putting it in the
buy box means the customer meets "no longer available" at checkout, which is
the worst place to meet it.

The same rule drives the "from" price on a product card. A card advertising
389 euros that leads to a page where the cheapest is 450 is the most
effective way there is to make people stop trusting a marketplace, and it
happens by caching that number and forgetting the offer behind it sold.

    from EUR 373.00
    cheapest excellent EUR 373.00
    cheapest good      EUR 401.00
    cheapest fair      EUR 410.00

Cheapest per grade is there because the question a customer actually asks is
not "what is the cheapest" but "what does an excellent one cost".

## The reservation is one statement

```sql
UPDATE offers
   SET state = 'reserved', reserved_by = $2, reserved_until = now() + $3
 WHERE id = $1
   AND (state = 'available'
     OR (state = 'reserved' AND reserved_until < now())
     OR (state = 'reserved' AND reserved_by = $2))
RETURNING ...
```

The condition is inside the write. A read to check availability followed by a
write to claim it loses the race this service exists to handle: two customers
adding the last unit within a few milliseconds both read `available`, both
write `reserved`, and the second write wins silently.

That is not a theory. `TestOnlyOneBasketCanHoldAUnit` fires sixteen
concurrent reservations at one offer and requires exactly one winner.
Replacing the statement above with a read followed by a write makes it fail
with **seven baskets holding the same physical phone**, which was measured
rather than imagined.

The expiry is in the same condition, not only in the sweep. A hold that has
run out is available immediately, because relying on a sweeper means the unit
is unbuyable for however long the sweep interval happens to be.

`reserved_by` appears in every write. Without it, any caller can release or
sell somebody else's reservation, and the bug that produces is a basket
emptying itself while another customer checks out.

The schema refuses a reserved row with no holder and no deadline, because
such a row is invisible to the sweep and the unit never comes back.

## When the unit is gone, offer the next one

The gap between a customer seeing a page and tapping add is long enough for a
unique unit to be taken, so that is routine rather than an edge case.
Stopping at "no longer available" makes the customer start again.

`Reserver.Add` falls back to the buy box choice among the remaining offers,
and it never substitutes silently and never substitutes downwards: somebody
who chose excellent did not agree to fair. The response says what was asked
for and what was given.

It makes one attempt at the substitute. Chasing a lost race further is a loop
with no natural end, and at that point showing the page again is the honest
answer.

## Trying it

    createdb catalogue
    DATABASE_URL=postgres://localhost/catalogue?sslmode=disable \
      go run ./cmd/cataloguectl -seed 7 -take 2

`-take` reserves the top offers one at a time so the buy box can be watched
moving, and the "from" price with it.

              offer   grade      price   battery  seller      score  state
    buy box   p1-o06  excellent  373.00  98%      4.7 (2400)  36700  available
              p1-o03  excellent  394.00  99%      4.7 (2400)  38800  available
              o001    good       401.00  90%      4.7 (2400)  41306  available
              p1-o05  fair       410.00  91%      unrated     45020  available

The fair unit at 410 ranks below the good one at 401, and the unrated
seller's offer sits lower than its price alone would put it. Both are the
table above, visible.

## Tests

    go test ./...                      26 cases without a database
    DATABASE_URL=... go test ./...     39, adding the Postgres suite

The in memory store is held to the same behaviour as Postgres, including
losing the sixteen-way race the same way, because a fake that allowed more
concurrency than the database does would hide exactly what the real one has
to survive.

## Where this stops

**Offers are not matched to products.** Every offer arrives with a
`product_id` already on it. In a real marketplace that matching is the first
hard problem, because nobody writes a product record: sellers list units and
the product is what several units turn out to have in common. It is a
normalisation problem large enough to be its own service.

No search, no facet counts across products, no pricing history.

The score weights are not learned from anything. A real one would be fitted
against conversion and returns, and would need the data to do it.
