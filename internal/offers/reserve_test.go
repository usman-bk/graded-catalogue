package offers_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/usmanvirk/graded-catalogue/internal/catalogue"
	"github.com/usmanvirk/graded-catalogue/internal/offers"
)

var sellers = map[string]catalogue.Seller{
	"s1": {ID: "s1", Rating: 4.6, RatingCount: 2000},
	"s2": {ID: "s2", Rating: 4.5, RatingCount: 1500},
}

func offer(id string, grade catalogue.Grade, price int64) catalogue.Offer {
	return catalogue.Offer{
		ID: id, ProductID: "p1", SellerID: "s1", Grade: grade,
		PriceCents: price, Currency: "EUR", WarrantyMonths: 12,
		State: catalogue.Available,
	}
}

func ctx() context.Context { return context.Background() }

func TestAddReservesIt(t *testing.T) {
	m := offers.NewMemory(offer("a", catalogue.GradeGood, 40000))
	r := offers.New(m)

	res, err := r.Add(ctx(), "a", "basket-1", sellers)
	if err != nil {
		t.Fatal(err)
	}
	if res.Substituted {
		t.Fatal("an available offer was substituted")
	}
	if res.Offer.ID != "a" || res.Offer.State != catalogue.Reserved {
		t.Fatalf("got %+v", res.Offer)
	}
}

// TestTakenOfferFallsBackToTheNextOne. With unique stock, the gap between
// seeing a page and tapping add is long enough for the unit to go, and that
// is routine rather than an edge case. Stopping at "no longer available"
// makes the customer start again.
func TestTakenOfferFallsBackToTheNextOne(t *testing.T) {
	m := offers.NewMemory(
		offer("wanted", catalogue.GradeGood, 40000),
		offer("next", catalogue.GradeGood, 41000),
	)
	r := offers.New(m)

	if _, err := m.Reserve(ctx(), "wanted", "somebody-else", time.Minute); err != nil {
		t.Fatal(err)
	}

	res, err := r.Add(ctx(), "wanted", "basket-1", sellers)
	if err != nil {
		t.Fatalf("the customer was turned away when another unit was available: %v", err)
	}
	if !res.Substituted {
		t.Fatal("a substitution happened and was not reported, so the customer is not told")
	}
	if res.Asked != "wanted" || res.Offer.ID != "next" {
		t.Fatalf("got %+v", res)
	}
}

// TestNeverSubstitutesDownwards. A customer who chose excellent did not
// agree to fair, whatever the price.
func TestNeverSubstitutesDownwards(t *testing.T) {
	m := offers.NewMemory(
		offer("wanted", catalogue.GradeExcellent, 45000),
		offer("worse", catalogue.GradeFair, 30000),
	)
	r := offers.New(m)

	if _, err := m.Reserve(ctx(), "wanted", "somebody-else", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add(ctx(), "wanted", "basket-1", sellers); err == nil {
		t.Fatal("a fair unit was substituted for an excellent one the customer chose")
	}
}

func TestSubstitutesUpwards(t *testing.T) {
	m := offers.NewMemory(
		offer("wanted", catalogue.GradeGood, 40000),
		offer("better", catalogue.GradeExcellent, 41000),
	)
	r := offers.New(m)

	if _, err := m.Reserve(ctx(), "wanted", "somebody-else", time.Minute); err != nil {
		t.Fatal(err)
	}
	res, err := r.Add(ctx(), "wanted", "basket-1", sellers)
	if err != nil {
		t.Fatal(err)
	}
	if res.Offer.ID != "better" {
		t.Fatalf("substituted %s", res.Offer.ID)
	}
}

func TestNothingToSubstituteReturnsTheOriginalError(t *testing.T) {
	m := offers.NewMemory(offer("only", catalogue.GradeGood, 40000))
	r := offers.New(m)

	if _, err := m.Reserve(ctx(), "only", "somebody-else", time.Minute); err != nil {
		t.Fatal(err)
	}
	_, err := r.Add(ctx(), "only", "basket-1", sellers)
	if !errors.Is(err, offers.ErrTaken) {
		t.Fatalf("got %v, want ErrTaken", err)
	}
}

func TestSoldOfferAlsoFallsBack(t *testing.T) {
	m := offers.NewMemory(
		offer("gone", catalogue.GradeGood, 40000),
		offer("next", catalogue.GradeGood, 41000),
	)
	r := offers.New(m)

	if _, err := m.Reserve(ctx(), "gone", "b0", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := m.Sell(ctx(), "gone", "b0"); err != nil {
		t.Fatal(err)
	}

	res, err := r.Add(ctx(), "gone", "basket-1", sellers)
	if err != nil {
		t.Fatalf("a sold offer did not fall back: %v", err)
	}
	if res.Offer.ID != "next" {
		t.Fatalf("substituted %s", res.Offer.ID)
	}
}

// TestTheSubstituteIsTheBuyBoxChoice, not simply the cheapest remaining.
func TestTheSubstituteIsTheBuyBoxChoice(t *testing.T) {
	m := offers.NewMemory(
		offer("wanted", catalogue.GradeGood, 40000),
		offer("cheap-fair", catalogue.GradeFair, 38000),
		offer("good-value", catalogue.GradeExcellent, 41000),
	)
	r := offers.New(m)

	if _, err := m.Reserve(ctx(), "wanted", "somebody-else", time.Minute); err != nil {
		t.Fatal(err)
	}
	res, err := r.Add(ctx(), "wanted", "basket-1", sellers)
	if err != nil {
		t.Fatal(err)
	}
	// cheap-fair is excluded for being a worse grade, so the excellent one
	// is the only eligible candidate.
	if res.Offer.ID != "good-value" {
		t.Fatalf("substituted %s", res.Offer.ID)
	}
}

func TestBuyRequiresTheReservation(t *testing.T) {
	m := offers.NewMemory(offer("a", catalogue.GradeGood, 40000))
	r := offers.New(m)

	if err := r.Buy(ctx(), "a", "basket-1"); err == nil {
		t.Fatal("an unreserved offer was bought")
	}
	if _, err := r.Add(ctx(), "a", "basket-1", sellers); err != nil {
		t.Fatal(err)
	}
	if err := r.Buy(ctx(), "a", "basket-2"); err == nil {
		t.Fatal("a basket bought a unit held by another basket")
	}
	if err := r.Buy(ctx(), "a", "basket-1"); err != nil {
		t.Fatal(err)
	}
}

func TestHoldsExpire(t *testing.T) {
	m := offers.NewMemory(offer("a", catalogue.GradeGood, 40000))
	base := time.Now()
	m.SetClock(func() time.Time { return base })

	r := offers.New(m)
	r.Hold = 15 * time.Minute
	if _, err := r.Add(ctx(), "a", "basket-1", sellers); err != nil {
		t.Fatal(err)
	}

	m.SetClock(func() time.Time { return base.Add(14 * time.Minute) })
	if _, err := m.Reserve(ctx(), "a", "basket-2", time.Minute); !errors.Is(err, offers.ErrTaken) {
		t.Fatalf("a live hold was taken: %v", err)
	}

	m.SetClock(func() time.Time { return base.Add(16 * time.Minute) })
	if _, err := m.Reserve(ctx(), "a", "basket-2", time.Minute); err != nil {
		t.Fatalf("a lapsed hold blocked a reservation: %v", err)
	}
}

func TestExpireHoldsCounts(t *testing.T) {
	m := offers.NewMemory(
		offer("a", catalogue.GradeGood, 40000),
		offer("b", catalogue.GradeGood, 41000),
		offer("c", catalogue.GradeGood, 42000),
	)
	base := time.Now()
	m.SetClock(func() time.Time { return base })

	for _, id := range []string{"a", "b"} {
		if _, err := m.Reserve(ctx(), id, "basket-1", 10*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Reserve(ctx(), "c", "basket-2", time.Hour); err != nil {
		t.Fatal(err)
	}

	m.SetClock(func() time.Time { return base.Add(20 * time.Minute) })
	n, err := m.ExpireHolds(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expired %d, want 2", n)
	}
	c, _ := m.Get(ctx(), "c")
	if c.State != catalogue.Reserved {
		t.Fatal("a hold inside its window was expired")
	}
}

// TestOnlyOneBasketWinsInMemoryToo. The fake must lose the race the same way
// the database does, or the tests above it prove nothing about production.
func TestOnlyOneBasketWinsInMemoryToo(t *testing.T) {
	m := offers.NewMemory(offer("a", catalogue.GradeGood, 40000))

	const n = 16
	var wg sync.WaitGroup
	won := make(chan string, n)
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if _, err := m.Reserve(ctx(), "a", fmt.Sprintf("b%02d", i), time.Minute); err == nil {
				won <- fmt.Sprintf("b%02d", i)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(won)

	var winners []string
	for w := range won {
		winners = append(winners, w)
	}
	if len(winners) != 1 {
		t.Fatalf("%d baskets hold one unit: %v", len(winners), winners)
	}
}
