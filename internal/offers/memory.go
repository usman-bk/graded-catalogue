package offers

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/usmanvirk/graded-catalogue/internal/catalogue"
)

// Memory is an in process Store.
//
// It exists so the substitution logic above can be tested without a
// database, and it holds one lock across everything rather than one per
// offer. That is the wrong shape for a real workload and the right shape
// for a fake: a fake that allowed more concurrency than Postgres does would
// hide the races the real store has to survive.
type Memory struct {
	mu     sync.Mutex
	offers map[string]*catalogue.Offer
	until  map[string]time.Time
	now    func() time.Time
}

func NewMemory(list ...catalogue.Offer) *Memory {
	m := &Memory{
		offers: make(map[string]*catalogue.Offer, len(list)),
		until:  map[string]time.Time{},
		now:    time.Now,
	}
	for _, o := range list {
		cp := o
		if cp.State == "" {
			cp.State = catalogue.Available
		}
		m.offers[cp.ID] = &cp
	}
	return m
}

func (m *Memory) SetClock(f func() time.Time) { m.mu.Lock(); m.now = f; m.mu.Unlock() }

// lapsed reports whether a reservation has run out. Checked on read, so a
// hold that has expired is available whether or not the sweep has run.
func (m *Memory) lapsed(o *catalogue.Offer) bool {
	if o.State != catalogue.Reserved {
		return false
	}
	u, ok := m.until[o.ID]
	return ok && m.now().After(u)
}

func (m *Memory) Reserve(ctx context.Context, offerID, basketID string, hold time.Duration) (catalogue.Offer, error) {
	if err := ctx.Err(); err != nil {
		return catalogue.Offer{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	o, ok := m.offers[offerID]
	if !ok {
		return catalogue.Offer{}, fmt.Errorf("no offer %s", offerID)
	}
	switch {
	case o.State == catalogue.Sold || o.State == catalogue.Withdrawn:
		return catalogue.Offer{}, ErrGone
	case o.State == catalogue.Reserved && o.ReservedBy != basketID && !m.lapsed(o):
		return catalogue.Offer{}, ErrTaken
	}

	o.State = catalogue.Reserved
	o.ReservedBy = basketID
	o.ReservedAt = m.now()
	m.until[offerID] = m.now().Add(hold)
	return *o, nil
}

func (m *Memory) Release(ctx context.Context, offerID, basketID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.offers[offerID]
	if !ok || o.State != catalogue.Reserved || o.ReservedBy != basketID {
		return ErrNotHeld
	}
	o.State = catalogue.Available
	o.ReservedBy = ""
	o.ReservedAt = time.Time{}
	delete(m.until, offerID)
	return nil
}

func (m *Memory) Extend(ctx context.Context, offerID, basketID string, hold time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.offers[offerID]
	if !ok || o.State != catalogue.Reserved || o.ReservedBy != basketID || m.lapsed(o) {
		return ErrNotHeld
	}
	m.until[offerID] = m.now().Add(hold)
	return nil
}

func (m *Memory) Sell(ctx context.Context, offerID, basketID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.offers[offerID]
	if !ok || o.State != catalogue.Reserved || o.ReservedBy != basketID || m.lapsed(o) {
		return ErrNotHeld
	}
	o.State = catalogue.Sold
	delete(m.until, offerID)
	return nil
}

func (m *Memory) ExpireHolds(ctx context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, o := range m.offers {
		if m.lapsed(o) {
			o.State = catalogue.Available
			o.ReservedBy = ""
			o.ReservedAt = time.Time{}
			delete(m.until, id)
			n++
		}
	}
	return n, nil
}

func (m *Memory) Get(ctx context.Context, offerID string) (catalogue.Offer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.offers[offerID]
	if !ok {
		return catalogue.Offer{}, fmt.Errorf("no offer %s", offerID)
	}
	return *o, nil
}

func (m *Memory) ByProduct(ctx context.Context, productID string) ([]catalogue.Offer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []catalogue.Offer
	for _, o := range m.offers {
		if o.ProductID == productID {
			cp := *o
			// A lapsed hold reads as available, the same as it does in the
			// database.
			if m.lapsed(o) {
				cp.State = catalogue.Available
				cp.ReservedBy = ""
			}
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

var _ Store = (*Memory)(nil)
