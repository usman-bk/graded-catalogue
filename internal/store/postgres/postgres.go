// Package postgres is the inventory, where the single unit behind each
// offer is actually claimed.
package postgres

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usmanvirk/graded-catalogue/internal/catalogue"
	"github.com/usmanvirk/graded-catalogue/internal/offers"
)

//go:embed schema.sql
var Schema string

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, Schema)
	return err
}

const offerCols = `id, product_id, seller_id, grade, price_cents, currency,
	battery_pct, warranty_months, state,
	coalesce(reserved_at, 'epoch'::timestamptz), coalesce(reserved_by, ''), listed_at`

func scanOffer(row pgx.Row) (catalogue.Offer, error) {
	var o catalogue.Offer
	var grade int16
	var battery, warranty int16
	err := row.Scan(&o.ID, &o.ProductID, &o.SellerID, &grade, &o.PriceCents, &o.Currency,
		&battery, &warranty, &o.State, &o.ReservedAt, &o.ReservedBy, &o.ListedAt)
	if err != nil {
		return catalogue.Offer{}, err
	}
	o.Grade = catalogue.Grade(grade)
	o.BatteryPct, o.WarrantyMonths = int(battery), int(warranty)
	return o, nil
}

// Reserve claims the unit.
//
// One statement, and the condition is inside the write. A read to check
// availability followed by a write to claim it loses the race this service
// exists to handle: two customers adding the last unit within a few
// milliseconds both read "available", both write "reserved", and the second
// write wins without anybody noticing until two people are promised one
// phone.
//
// The expiry is also handled here rather than only by the sweep, through
// the `reserved_until < now()` branch. A hold that has run out is available
// whether or not the sweep has got to it, and relying on the sweep means a
// unit is unbuyable for however long the sweep interval happens to be.
func (s *Store) Reserve(ctx context.Context, offerID, basketID string, hold time.Duration) (catalogue.Offer, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE offers
		   SET state = 'reserved',
		       reserved_at = now(),
		       reserved_by = $2,
		       reserved_until = now() + $3::interval
		 WHERE id = $1
		   AND (
		         state = 'available'
		      OR (state = 'reserved' AND reserved_until < now())
		      OR (state = 'reserved' AND reserved_by = $2)
		       )
		RETURNING `+offerCols,
		offerID, basketID, interval(hold))

	o, err := scanOffer(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// Nothing was updated. Why matters to the caller, so the current
		// state is read to tell "somebody else has it" from "it is sold".
		cur, getErr := s.Get(ctx, offerID)
		if getErr != nil {
			return catalogue.Offer{}, getErr
		}
		switch cur.State {
		case catalogue.Sold, catalogue.Withdrawn:
			return catalogue.Offer{}, offers.ErrGone
		default:
			return catalogue.Offer{}, offers.ErrTaken
		}
	}
	if err != nil {
		return catalogue.Offer{}, fmt.Errorf("reserving %s: %w", offerID, err)
	}
	return o, nil
}

// Release gives the unit back, and only to the basket holding it.
//
// The reserved_by condition is not politeness. Without it, any caller can
// release anybody's reservation, and the bug that produces is a customer's
// basket emptying itself while somebody else checks out.
func (s *Store) Release(ctx context.Context, offerID, basketID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE offers
		   SET state = 'available', reserved_at = NULL, reserved_by = NULL, reserved_until = NULL
		 WHERE id = $1 AND state = 'reserved' AND reserved_by = $2`,
		offerID, basketID)
	if err != nil {
		return fmt.Errorf("releasing %s: %w", offerID, err)
	}
	if tag.RowsAffected() == 0 {
		return offers.ErrNotHeld
	}
	return nil
}

func (s *Store) Extend(ctx context.Context, offerID, basketID string, hold time.Duration) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE offers
		   SET reserved_until = now() + $3::interval
		 WHERE id = $1 AND state = 'reserved' AND reserved_by = $2 AND reserved_until >= now()`,
		offerID, basketID, interval(hold))
	if err != nil {
		return fmt.Errorf("extending %s: %w", offerID, err)
	}
	if tag.RowsAffected() == 0 {
		// An expired hold is not extended. Renewing one that has already
		// lapsed would take the unit back from whoever picked it up.
		return offers.ErrNotHeld
	}
	return nil
}

// Sell is the same shape as the others: conditional, single statement, and
// only for the basket that holds it.
func (s *Store) Sell(ctx context.Context, offerID, basketID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE offers
		   SET state = 'sold', reserved_until = NULL
		 WHERE id = $1 AND state = 'reserved' AND reserved_by = $2 AND reserved_until >= now()`,
		offerID, basketID)
	if err != nil {
		return fmt.Errorf("selling %s: %w", offerID, err)
	}
	if tag.RowsAffected() == 0 {
		return offers.ErrNotHeld
	}
	return nil
}

// ExpireHolds returns lapsed reservations to the market.
//
// Nothing else does. If this stops running, abandoned baskets accumulate
// and the marketplace looks like it has sold out, which is a failure that
// shows up as falling revenue rather than as an error anywhere.
func (s *Store) ExpireHolds(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE offers
		   SET state = 'available', reserved_at = NULL, reserved_by = NULL, reserved_until = NULL
		 WHERE state = 'reserved' AND reserved_until < now()`)
	if err != nil {
		return 0, fmt.Errorf("expiring holds: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *Store) Get(ctx context.Context, offerID string) (catalogue.Offer, error) {
	o, err := scanOffer(s.pool.QueryRow(ctx, `SELECT `+offerCols+` FROM offers WHERE id = $1`, offerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return catalogue.Offer{}, fmt.Errorf("no offer %s", offerID)
	}
	return o, err
}

func (s *Store) ByProduct(ctx context.Context, productID string) ([]catalogue.Offer, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+offerCols+` FROM offers WHERE product_id = $1 ORDER BY id`, productID)
	if err != nil {
		return nil, fmt.Errorf("offers for %s: %w", productID, err)
	}
	defer rows.Close()

	var out []catalogue.Offer
	for rows.Next() {
		o, err := scanOffer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Sellers loads the sellers behind a set of offers, in one statement.
func (s *Store) Sellers(ctx context.Context, ids []string) (map[string]catalogue.Seller, error) {
	out := map[string]catalogue.Seller{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, rating, rating_count, dispatch_days FROM sellers WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("loading sellers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sel catalogue.Seller
		var rating float32
		var dispatch int16
		if err := rows.Scan(&sel.ID, &sel.Name, &rating, &sel.RatingCount, &dispatch); err != nil {
			return nil, err
		}
		sel.Rating, sel.DispatchDays = float64(rating), int(dispatch)
		out[sel.ID] = sel
	}
	return out, rows.Err()
}

// PutProduct and PutSeller and PutOffer exist so the tests and the loader
// have a way in. There is no update path for an offer's price here, because
// a price change on a unique unit is a new listing rather than an edit.
func (s *Store) PutProduct(ctx context.Context, p catalogue.Product) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO products (id, title, brand, model, storage, colour)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (id) DO UPDATE SET title = EXCLUDED.title`,
		p.ID, p.Title, p.Brand, p.Model, p.Storage, p.Colour)
	return err
}

func (s *Store) PutSeller(ctx context.Context, sel catalogue.Seller) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO sellers (id, name, rating, rating_count, dispatch_days)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (id) DO UPDATE
		    SET rating = EXCLUDED.rating, rating_count = EXCLUDED.rating_count`,
		sel.ID, sel.Name, sel.Rating, sel.RatingCount, sel.DispatchDays)
	return err
}

func (s *Store) PutOffer(ctx context.Context, o catalogue.Offer) error {
	if err := o.Validate(); err != nil {
		return err
	}
	state := o.State
	if state == "" {
		state = catalogue.Available
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO offers (id, product_id, seller_id, grade, price_cents, currency,
		                     battery_pct, warranty_months, state)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (id) DO NOTHING`,
		o.ID, o.ProductID, o.SellerID, int16(o.Grade), o.PriceCents, o.Currency,
		int16(o.BatteryPct), int16(o.WarrantyMonths), state)
	return err
}

func interval(d time.Duration) string {
	return fmt.Sprintf("%d seconds", int(d.Seconds()))
}

var _ offers.Store = (*Store)(nil)
