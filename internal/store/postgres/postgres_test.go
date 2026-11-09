package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usmanvirk/graded-catalogue/internal/catalogue"
	"github.com/usmanvirk/graded-catalogue/internal/offers"
	"github.com/usmanvirk/graded-catalogue/internal/store/postgres"
)

// A real database or nothing. What is under test is a conditional UPDATE
// under concurrency, and a mock of a database cannot lose the race that the
// real one has to win.
func fresh(t *testing.T) (*postgres.Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(pool.Close)

	s := postgres.New(pool)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE offers, products, sellers CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s, pool
}

func seed(t *testing.T, s *postgres.Store, n int) {
	t.Helper()
	ctx := context.Background()
	if err := s.PutProduct(ctx, catalogue.Product{
		ID: "p1", Title: "iPhone 13 Pro 256 GB", Brand: "Apple", Model: "iPhone 13 Pro",
	}); err != nil {
		t.Fatal(err)
	}
	for _, sel := range []catalogue.Seller{
		{ID: "s1", Name: "Renewed GmbH", Rating: 4.6, RatingCount: 2400, DispatchDays: 1},
		{ID: "s2", Name: "CircuitSecond", Rating: 4.4, RatingCount: 800, DispatchDays: 2},
	} {
		if err := s.PutSeller(ctx, sel); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		grade := catalogue.GradeGood
		if i%3 == 0 {
			grade = catalogue.GradeExcellent
		}
		sellerID := "s1"
		if i%2 == 0 {
			sellerID = "s2"
		}
		if err := s.PutOffer(ctx, catalogue.Offer{
			ID: fmt.Sprintf("o%03d", i), ProductID: "p1", SellerID: sellerID,
			Grade: grade, PriceCents: int64(40000 + i*100), Currency: "EUR",
			BatteryPct: 90, WarrantyMonths: 12,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	s, _ := fresh(t)
	for i := 0; i < 3; i++ {
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
}

func TestReserveThenRelease(t *testing.T) {
	s, _ := fresh(t)
	seed(t, s, 3)
	ctx := context.Background()

	o, err := s.Reserve(ctx, "o000", "basket-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if o.State != catalogue.Reserved || o.ReservedBy != "basket-a" {
		t.Fatalf("after reserving, the offer is %+v", o)
	}

	if err := s.Release(ctx, "o000", "basket-a"); err != nil {
		t.Fatal(err)
	}
	back, err := s.Get(ctx, "o000")
	if err != nil {
		t.Fatal(err)
	}
	if back.State != catalogue.Available {
		t.Fatalf("after releasing, the offer is %s", back.State)
	}
}

// TestOnlyOneBasketCanHoldAUnit is the whole point of the repository. With
// a quantity of one, two customers adding the last unit within a few
// milliseconds is routine, and a read followed by a write promises one
// phone to two people.
func TestOnlyOneBasketCanHoldAUnit(t *testing.T) {
	s, _ := fresh(t)
	seed(t, s, 1)

	const baskets = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	won := make(chan string, baskets)
	lost := make(chan error, baskets)

	for i := 0; i < baskets; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := s.Reserve(context.Background(), "o000", fmt.Sprintf("basket-%02d", i), time.Minute)
			if err == nil {
				won <- fmt.Sprintf("basket-%02d", i)
				return
			}
			lost <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(won)
	close(lost)

	var winners []string
	for w := range won {
		winners = append(winners, w)
	}
	if len(winners) != 1 {
		t.Fatalf("%d baskets hold the same physical phone: %v", len(winners), winners)
	}
	n := 0
	for err := range lost {
		n++
		if !errors.Is(err, offers.ErrTaken) {
			t.Fatalf("a loser got %v, want ErrTaken, which the caller treats as a normal race", err)
		}
	}
	if n != baskets-1 {
		t.Fatalf("%d losers, want %d", n, baskets-1)
	}
}

// TestReservingTwiceFromTheSameBasketIsFine. A customer who taps add twice
// has not lost their reservation.
func TestReservingTwiceFromTheSameBasketIsFine(t *testing.T) {
	s, _ := fresh(t)
	seed(t, s, 1)
	ctx := context.Background()

	if _, err := s.Reserve(ctx, "o000", "basket-a", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(ctx, "o000", "basket-a", time.Minute); err != nil {
		t.Fatalf("the same basket could not re-reserve what it holds: %v", err)
	}
}

// TestAnotherBasketCannotReleaseYourReservation. Without the reserved_by
// condition, any caller can empty somebody else's basket.
func TestAnotherBasketCannotReleaseYourReservation(t *testing.T) {
	s, _ := fresh(t)
	seed(t, s, 1)
	ctx := context.Background()

	if _, err := s.Reserve(ctx, "o000", "basket-a", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, "o000", "basket-b"); !errors.Is(err, offers.ErrNotHeld) {
		t.Fatalf("basket-b released basket-a's reservation: %v", err)
	}
	o, _ := s.Get(ctx, "o000")
	if o.State != catalogue.Reserved || o.ReservedBy != "basket-a" {
		t.Fatalf("the reservation was disturbed: %+v", o)
	}
}

func TestSoldIsGoneAndSaysSo(t *testing.T) {
	s, _ := fresh(t)
	seed(t, s, 1)
	ctx := context.Background()

	if _, err := s.Reserve(ctx, "o000", "basket-a", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.Sell(ctx, "o000", "basket-a"); err != nil {
		t.Fatal(err)
	}

	// ErrGone and not ErrTaken: one of them may come back and the other
	// will not, and the caller shows a different thing for each.
	_, err := s.Reserve(ctx, "o000", "basket-b", time.Minute)
	if !errors.Is(err, offers.ErrGone) {
		t.Fatalf("reserving a sold unit gave %v, want ErrGone", err)
	}
}

func TestOnlyTheHolderCanSell(t *testing.T) {
	s, _ := fresh(t)
	seed(t, s, 1)
	ctx := context.Background()

	if _, err := s.Reserve(ctx, "o000", "basket-a", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.Sell(ctx, "o000", "basket-b"); !errors.Is(err, offers.ErrNotHeld) {
		t.Fatalf("basket-b sold a unit held by basket-a: %v", err)
	}
}

// TestAnExpiredHoldIsAvailableImmediately. Relying on the sweep means the
// unit is unbuyable for however long the sweep interval is, which is a
// customer being told no while the thing is for sale.
func TestAnExpiredHoldIsAvailableImmediately(t *testing.T) {
	s, pool := fresh(t)
	seed(t, s, 1)
	ctx := context.Background()

	if _, err := s.Reserve(ctx, "o000", "basket-a", time.Hour); err != nil {
		t.Fatal(err)
	}
	// Age the hold rather than waiting an hour.
	if _, err := pool.Exec(ctx,
		`UPDATE offers SET reserved_until = now() - interval '1 minute' WHERE id = 'o000'`); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Reserve(ctx, "o000", "basket-b", time.Minute); err != nil {
		t.Fatalf("a lapsed hold blocked a new reservation, with no sweep having run: %v", err)
	}
	o, _ := s.Get(ctx, "o000")
	if o.ReservedBy != "basket-b" {
		t.Fatalf("the offer is held by %q", o.ReservedBy)
	}
}

func TestExpireHoldsReturnsStockToTheMarket(t *testing.T) {
	s, pool := fresh(t)
	seed(t, s, 4)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := s.Reserve(ctx, fmt.Sprintf("o%03d", i), "basket-a", time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	// Two of the three have lapsed.
	if _, err := pool.Exec(ctx,
		`UPDATE offers SET reserved_until = now() - interval '1 minute' WHERE id IN ('o000','o001')`); err != nil {
		t.Fatal(err)
	}

	n, err := s.ExpireHolds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expired %d holds, want 2", n)
	}
	still, _ := s.Get(ctx, "o002")
	if still.State != catalogue.Reserved {
		t.Fatal("a hold inside its window was expired, taking a unit out of somebody's basket")
	}
	back, _ := s.Get(ctx, "o000")
	if back.State != catalogue.Available || back.ReservedBy != "" {
		t.Fatalf("an expired hold left the row as %+v", back)
	}
}

// TestExtendCannotRaiseTheDead. Renewing a hold that has already lapsed
// takes the unit back from whoever picked it up.
func TestExtendCannotRaiseTheDead(t *testing.T) {
	s, pool := fresh(t)
	seed(t, s, 1)
	ctx := context.Background()

	if _, err := s.Reserve(ctx, "o000", "basket-a", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.Extend(ctx, "o000", "basket-a", 2*time.Hour); err != nil {
		t.Fatalf("extending a live hold failed: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE offers SET reserved_until = now() - interval '1 second' WHERE id = 'o000'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Extend(ctx, "o000", "basket-a", time.Hour); !errors.Is(err, offers.ErrNotHeld) {
		t.Fatalf("an expired hold was extended: %v", err)
	}
}

// TestTheSchemaRefusesAnIncompleteReservation. A reserved row with no
// deadline is invisible to the sweep, so the unit never returns.
func TestTheSchemaRefusesAnIncompleteReservation(t *testing.T) {
	s, pool := fresh(t)
	seed(t, s, 1)
	ctx := context.Background()

	_, err := pool.Exec(ctx, `UPDATE offers SET state = 'reserved' WHERE id = 'o000'`)
	if err == nil {
		t.Fatal("a reserved row with no holder and no deadline was accepted, and the sweep can never free it")
	}
	_ = s
}

func TestByProductAndSellers(t *testing.T) {
	s, _ := fresh(t)
	seed(t, s, 6)
	ctx := context.Background()

	all, err := s.ByProduct(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 6 {
		t.Fatalf("%d offers, want 6", len(all))
	}

	ids := make([]string, 0, len(all))
	for _, o := range all {
		ids = append(ids, o.SellerID)
	}
	sellers, err := s.Sellers(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(sellers) != 2 {
		t.Fatalf("%d sellers, want 2", len(sellers))
	}
	if sellers["s1"].Rating == 0 || sellers["s1"].RatingCount == 0 {
		t.Fatalf("seller s1 came back empty: %+v", sellers["s1"])
	}
}

// TestTheBuyBoxOnlyEverShowsSomethingBuyable, end to end against the
// database: reserve the winner and the next one must take its place.
func TestTheBuyBoxMovesOnWhenTheWinnerIsTaken(t *testing.T) {
	s, _ := fresh(t)
	seed(t, s, 8)
	ctx := context.Background()

	all, _ := s.ByProduct(ctx, "p1")
	ids := make([]string, 0, len(all))
	for _, o := range all {
		ids = append(ids, o.SellerID)
	}
	sellers, _ := s.Sellers(ctx, ids)

	first, _, ok := catalogue.BuyBox(all, sellers)
	if !ok {
		t.Fatal("no buy box")
	}
	if _, err := s.Reserve(ctx, first.ID, "basket-a", time.Minute); err != nil {
		t.Fatal(err)
	}

	all, _ = s.ByProduct(ctx, "p1")
	second, _, ok := catalogue.BuyBox(all, sellers)
	if !ok {
		t.Fatal("no buy box after the winner was taken")
	}
	if second.ID == first.ID {
		t.Fatal("a reserved offer is still in the buy box, so the customer meets it at checkout")
	}
}
