// Package offers holds and releases the single unit behind an offer.
//
// With a quantity of one, putting something in a basket is not a note to
// self, it is a claim on a physical object that nobody else can have. That
// makes the basket part of the inventory system rather than a view on it,
// which is the main way this differs from a shop selling new goods.
package offers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/usmanvirk/graded-catalogue/internal/catalogue"
)

// Hold is how long a reservation lasts without being renewed.
//
// Long enough to finish a checkout on a phone on a train, short enough that
// an abandoned basket does not keep a sellable unit off the market for a
// day. Fifteen minutes is the usual answer and it is a product decision,
// which is why it is a parameter rather than a constant in the query.
const DefaultHold = 15 * time.Minute

var (
	// ErrTaken means somebody else has it. It is the normal outcome of a
	// race, not a failure, and the caller shows the next best offer rather
	// than an error.
	ErrTaken = errors.New("offer is already held")
	// ErrGone means sold or withdrawn: it is not coming back.
	ErrGone = errors.New("offer is no longer for sale")
	// ErrNotHeld means the caller tried to release or buy something it does
	// not hold, which is a bug in the caller or an expired hold.
	ErrNotHeld = errors.New("offer is not held by this basket")
)

// Store is the inventory.
//
// Every method is a single statement against the database, and that is the
// design rather than an implementation detail. A read followed by a write
// loses the race that matters here: two people adding the last unit to a
// basket within the same few milliseconds both see "available" and both
// write "reserved", and the second write wins silently. The condition has
// to be inside the write.
type Store interface {
	// Reserve claims the offer for a basket. It returns ErrTaken if
	// somebody already holds it and ErrGone if it is sold or withdrawn.
	Reserve(ctx context.Context, offerID, basketID string, hold time.Duration) (catalogue.Offer, error)

	// Release gives it back, and only to the basket holding it.
	Release(ctx context.Context, offerID, basketID string) error

	// Extend renews a hold the basket already has, for a checkout that is
	// taking a while.
	Extend(ctx context.Context, offerID, basketID string, hold time.Duration) error

	// Sell marks it sold. Only the basket holding it may.
	Sell(ctx context.Context, offerID, basketID string) error

	// ExpireHolds releases reservations past their hold and reports how
	// many came back. Nothing else returns stock to the market, so this not
	// running is indistinguishable from the marketplace running out.
	ExpireHolds(ctx context.Context) (int64, error)

	Get(ctx context.Context, offerID string) (catalogue.Offer, error)
	ByProduct(ctx context.Context, productID string) ([]catalogue.Offer, error)
}

// Reserver puts the policy around the store.
type Reserver struct {
	Store Store
	Hold  time.Duration
}

func New(s Store) *Reserver { return &Reserver{Store: s, Hold: DefaultHold} }

// Result says what happened when a customer tried to add something.
type Result struct {
	Offer catalogue.Offer
	// Substituted is set when the offer asked for was gone and another was
	// taken instead.
	Substituted bool
	// Asked is the offer originally wanted, when a substitution happened.
	Asked string
}

// Add reserves an offer, and falls back to the next best one for the same
// product if it has just gone.
//
// The fallback is the point. With unique stock, the gap between a customer
// seeing a page and tapping add is long enough for the unit to be taken,
// and that is a routine event rather than an edge case. Showing "no longer
// available" and stopping makes the customer start again; offering the next
// one, and saying so, keeps the sale and tells the truth.
//
// It never substitutes silently and never substitutes to a worse grade,
// because a customer who chose excellent did not agree to fair.
func (r *Reserver) Add(ctx context.Context, offerID, basketID string, sellers map[string]catalogue.Seller) (Result, error) {
	o, err := r.Store.Reserve(ctx, offerID, basketID, r.Hold)
	if err == nil {
		return Result{Offer: o}, nil
	}
	if !errors.Is(err, ErrTaken) && !errors.Is(err, ErrGone) {
		return Result{}, err
	}

	wanted, getErr := r.Store.Get(ctx, offerID)
	if getErr != nil {
		return Result{}, err
	}

	siblings, listErr := r.Store.ByProduct(ctx, wanted.ProductID)
	if listErr != nil {
		return Result{}, err
	}

	// Same product, and no worse than what was chosen.
	var eligible []catalogue.Offer
	for _, s := range siblings {
		if s.ID != offerID && s.State == catalogue.Available && s.Grade >= wanted.Grade {
			eligible = append(eligible, s)
		}
	}
	best, _, ok := catalogue.BuyBox(eligible, sellers)
	if !ok {
		return Result{}, err
	}

	// Reserving the substitute can lose its own race, and chasing that
	// further would be a loop with no natural end. One attempt, and the
	// customer sees the page again.
	got, err2 := r.Store.Reserve(ctx, best.ID, basketID, r.Hold)
	if err2 != nil {
		return Result{}, err
	}
	return Result{Offer: got, Substituted: true, Asked: offerID}, nil
}

// Buy completes a purchase from a held reservation.
func (r *Reserver) Buy(ctx context.Context, offerID, basketID string) error {
	if err := r.Store.Sell(ctx, offerID, basketID); err != nil {
		return fmt.Errorf("buying %s: %w", offerID, err)
	}
	return nil
}
