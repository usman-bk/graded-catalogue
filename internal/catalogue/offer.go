// Package catalogue models a marketplace where every unit is one of a kind.
//
// A shop selling new goods has a SKU with a quantity behind it, and any two
// units of that SKU are the same thing. A refurbished marketplace does not.
// Two iPhone 13 Pros in 256 GB are different objects: different casings,
// different battery health, different sellers, different warranties and
// different prices. Quantity is always one.
//
// Almost every awkward thing in this package follows from that sentence.
package catalogue

import (
	"fmt"
	"strings"
	"time"
)

// Grade is the cosmetic condition, which is the main axis two otherwise
// identical units differ on.
//
// An ordered small set, not free text. Sellers given a text box produce
// "Very Good (A+)", "like new" and "mint", and none of those can be sorted,
// filtered or compared, which makes the catalogue unusable for the one
// decision the customer is actually making.
type Grade int

const (
	GradeUnknown Grade = iota
	GradeFair
	GradeGood
	GradeExcellent
)

var gradeNames = map[Grade]string{
	GradeFair:      "fair",
	GradeGood:      "good",
	GradeExcellent: "excellent",
}

func (g Grade) String() string {
	if n, ok := gradeNames[g]; ok {
		return n
	}
	return "unknown"
}

func (g Grade) Valid() bool { _, ok := gradeNames[g]; return ok }

func ParseGrade(s string) (Grade, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "fair":
		return GradeFair, nil
	case "good":
		return GradeGood, nil
	case "excellent":
		return GradeExcellent, nil
	default:
		return GradeUnknown, fmt.Errorf("unknown grade %q", s)
	}
}

// State is where an offer is in its life. There is no quantity field: an
// offer is one physical object, so the only question is whether it is still
// available.
type State string

const (
	Available State = "available"
	// Reserved means somebody has it in a basket. It is not sold, and it is
	// not available either, and a catalogue that only has those two states
	// sells the same phone twice.
	Reserved  State = "reserved"
	Sold      State = "sold"
	Withdrawn State = "withdrawn"
)

// Offer is one physical unit a seller has listed.
type Offer struct {
	ID        string
	ProductID string
	SellerID  string

	Grade          Grade
	PriceCents     int64
	Currency       string
	BatteryPct     int // 0 when the unit has no battery, such as a monitor
	WarrantyMonths int

	State      State
	ReservedAt time.Time
	// ReservedBy is the basket holding it, so a reservation can be released
	// by the right owner rather than by whoever asks first.
	ReservedBy string

	ListedAt time.Time
}

func (o Offer) Validate() error {
	switch {
	case o.ID == "":
		return fmt.Errorf("offer has no id")
	case o.ProductID == "":
		return fmt.Errorf("offer %s has no product", o.ID)
	case o.SellerID == "":
		return fmt.Errorf("offer %s has no seller", o.ID)
	case !o.Grade.Valid():
		return fmt.Errorf("offer %s has grade %q", o.ID, o.Grade)
	case o.PriceCents <= 0:
		return fmt.Errorf("offer %s is priced at %d", o.ID, o.PriceCents)
	case o.Currency == "":
		return fmt.Errorf("offer %s has no currency", o.ID)
	case o.BatteryPct < 0 || o.BatteryPct > 100:
		return fmt.Errorf("offer %s reports %d%% battery", o.ID, o.BatteryPct)
	}
	return nil
}

// Seller is what is known about the merchant, which is part of whether an
// offer is the one to show.
type Seller struct {
	ID           string
	Name         string
	Rating       float64 // out of 5
	RatingCount  int
	DispatchDays int
}

// Product is what a customer searches for, and it does not exist in its own
// right.
//
// Nobody writes a product record. Sellers list units, and the product is
// what several units turn out to have in common, so it is derived rather
// than owned. That is the opposite of a retailer's catalogue and it is why
// matching an offer to a product is the first hard problem here rather than
// a lookup.
type Product struct {
	ID      string
	Title   string
	Brand   string
	Model   string
	Storage string
	Colour  string
}
