package catalogue_test

import (
	"math/rand"
	"testing"

	"github.com/usmanvirk/graded-catalogue/internal/catalogue"
)

func offer(id string, grade catalogue.Grade, price int64, opts ...func(*catalogue.Offer)) catalogue.Offer {
	o := catalogue.Offer{
		ID: id, ProductID: "p1", SellerID: "good-seller",
		Grade: grade, PriceCents: price, Currency: "EUR",
		State: catalogue.Available, WarrantyMonths: 12,
	}
	for _, f := range opts {
		f(&o)
	}
	return o
}

func seller(id string) func(*catalogue.Offer) {
	return func(o *catalogue.Offer) { o.SellerID = id }
}
func battery(pct int) func(*catalogue.Offer) {
	return func(o *catalogue.Offer) { o.BatteryPct = pct }
}
func state(s catalogue.State) func(*catalogue.Offer) {
	return func(o *catalogue.Offer) { o.State = s }
}
func warranty(m int) func(*catalogue.Offer) {
	return func(o *catalogue.Offer) { o.WarrantyMonths = m }
}

var sellers = map[string]catalogue.Seller{
	"good-seller":  {ID: "good-seller", Rating: 4.6, RatingCount: 2400},
	"other-seller": {ID: "other-seller", Rating: 4.5, RatingCount: 900},
	"new-seller":   {ID: "new-seller", Rating: 0, RatingCount: 0},
	"poor-seller":  {ID: "poor-seller", Rating: 3.2, RatingCount: 800},
}

func TestParseGrade(t *testing.T) {
	for in, want := range map[string]catalogue.Grade{
		"fair": catalogue.GradeFair, "good": catalogue.GradeGood,
		"excellent": catalogue.GradeExcellent, "  Excellent ": catalogue.GradeExcellent,
	} {
		got, err := catalogue.ParseGrade(in)
		if err != nil || got != want {
			t.Errorf("%q parsed as %v (%v)", in, got, err)
		}
	}
	for _, bad := range []string{"", "mint", "A+", "Very Good", "like new"} {
		if _, err := catalogue.ParseGrade(bad); err == nil {
			t.Errorf("%q was accepted as a grade", bad)
		}
	}
}

func TestGradesAreOrdered(t *testing.T) {
	if !(catalogue.GradeFair < catalogue.GradeGood && catalogue.GradeGood < catalogue.GradeExcellent) {
		t.Fatal("grades do not sort, so they cannot be filtered or compared")
	}
}

// TestTheBuyBoxIsNotJustTheCheapest is the decision the whole ranking is
// about. A fair unit 20 euros under an excellent one is not a better deal,
// and a customer shown the cheapest thing assumes it is.
func TestTheBuyBoxIsNotJustTheCheapest(t *testing.T) {
	cheapFair := offer("fair-cheap", catalogue.GradeFair, 38000)
	dearExcellent := offer("exc", catalogue.GradeExcellent, 40000)

	winner, _, ok := catalogue.BuyBox([]catalogue.Offer{cheapFair, dearExcellent}, sellers)
	if !ok {
		t.Fatal("no winner")
	}
	if winner.ID != "exc" {
		t.Fatalf("the buy box picked %s, the cheaper and worse unit", winner.ID)
	}
}

// TestPriceStillWinsWhenTheGapIsLarge. The grade adjustment must not be so
// strong that the top grade always wins whatever it costs, because then it
// is not a ranking, it is a sort by grade.
func TestPriceStillWinsWhenTheGapIsLarge(t *testing.T) {
	cheapGood := offer("good-cheap", catalogue.GradeGood, 30000)
	dearExcellent := offer("exc-dear", catalogue.GradeExcellent, 40000)

	winner, _, _ := catalogue.BuyBox([]catalogue.Offer{cheapGood, dearExcellent}, sellers)
	if winner.ID != "good-cheap" {
		t.Fatalf("the buy box picked %s: 100 euros is not worth one grade", winner.ID)
	}
}

func TestReservedAndSoldOffersAreNotShown(t *testing.T) {
	all := []catalogue.Offer{
		offer("sold-cheap", catalogue.GradeExcellent, 20000, state(catalogue.Sold)),
		offer("held-cheap", catalogue.GradeExcellent, 21000, state(catalogue.Reserved)),
		offer("withdrawn", catalogue.GradeExcellent, 22000, state(catalogue.Withdrawn)),
		offer("live", catalogue.GradeGood, 40000),
	}
	winner, rest, ok := catalogue.BuyBox(all, sellers)
	if !ok || winner.ID != "live" {
		t.Fatalf("the buy box picked %s, which is not for sale", winner.ID)
	}
	if len(rest) != 0 {
		t.Fatalf("offers that are not for sale appeared in the list: %v", rest)
	}
}

// A reserved offer is in somebody's basket and may come back. Showing it
// means the customer meets "no longer available" at checkout, which is the
// worst place to meet it.
func TestNoAvailableOffersMeansNoBuyBox(t *testing.T) {
	_, _, ok := catalogue.BuyBox([]catalogue.Offer{
		offer("a", catalogue.GradeGood, 1000, state(catalogue.Sold)),
	}, sellers)
	if ok {
		t.Fatal("a product with nothing for sale produced a buy box")
	}
	if _, _, ok := catalogue.BuyBox(nil, sellers); ok {
		t.Fatal("no offers at all produced a buy box")
	}
}

// TestAnUnratedSellerIsNotFirstOnPriceAlone. A new seller is not a bad
// seller, and putting them at the top on price is a bet made with the
// customer's money.
func TestAnUnratedSellerIsNotFirstOnPriceAlone(t *testing.T) {
	newbie := offer("new", catalogue.GradeGood, 39900, seller("new-seller"))
	known := offer("known", catalogue.GradeGood, 40000, seller("good-seller"))

	winner, _, _ := catalogue.BuyBox([]catalogue.Offer{newbie, known}, sellers)
	if winner.ID != "known" {
		t.Fatalf("an unrated seller won on a one euro difference")
	}

	// And the penalty is not infinite: a big enough saving still wins, or
	// a new seller can never get started.
	cheapNewbie := offer("new-cheap", catalogue.GradeGood, 35000, seller("new-seller"))
	winner, _, _ = catalogue.BuyBox([]catalogue.Offer{cheapNewbie, known}, sellers)
	if winner.ID != "new-cheap" {
		t.Fatal("a new seller 50 euros cheaper still lost, so no new seller can ever win")
	}
}

func TestAPoorlyRatedSellerIsPenalised(t *testing.T) {
	poor := offer("poor", catalogue.GradeGood, 39900, seller("poor-seller"))
	good := offer("good", catalogue.GradeGood, 40000, seller("good-seller"))
	winner, _, _ := catalogue.BuyBox([]catalogue.Offer{poor, good}, sellers)
	if winner.ID != "good" {
		t.Fatal("a 3.2 rated seller won on a one euro difference")
	}
}

func TestAWeakBatteryIsPenalisedOnlyWhereThereIsOne(t *testing.T) {
	weak := offer("weak", catalogue.GradeGood, 39500, battery(78))
	strong := offer("strong", catalogue.GradeGood, 40000, battery(95))
	winner, _, _ := catalogue.BuyBox([]catalogue.Offer{weak, strong}, sellers)
	if winner.ID != "strong" {
		t.Fatalf("a 78%% battery won on five euros")
	}

	// A monitor has no battery, and a zero must not be read as a dead one.
	noBattery := offer("monitor", catalogue.GradeGood, 39500, battery(0))
	other := offer("other", catalogue.GradeGood, 40000, battery(0))
	winner, _, _ = catalogue.BuyBox([]catalogue.Offer{noBattery, other}, sellers)
	if winner.ID != "monitor" {
		t.Fatal("an item with no battery was penalised for having no battery")
	}
}

func TestALongerWarrantyHelpsButIsCapped(t *testing.T) {
	short := offer("short", catalogue.GradeGood, 40000, warranty(6))
	long := offer("long", catalogue.GradeGood, 40000, warranty(24))
	winner, _, _ := catalogue.BuyBox([]catalogue.Offer{short, long}, sellers)
	if winner.ID != "long" {
		t.Fatal("the longer warranty lost at the same price")
	}

	// A seller claiming ten years must not be able to buy the buy box with
	// a number in a text field.
	absurd := offer("absurd", catalogue.GradeGood, 45000, warranty(120))
	sane := offer("sane", catalogue.GradeGood, 40000, warranty(24))
	winner, _, _ = catalogue.BuyBox([]catalogue.Offer{absurd, sane}, sellers)
	if winner.ID != "sane" {
		t.Fatal("a 120 month warranty claim won the buy box from 50 euros away")
	}
}

// TestTheRankingIsStable. A buy box that moves between two page loads looks
// broken to a customer and looks rigged to a seller.
func TestTheRankingIsStable(t *testing.T) {
	all := []catalogue.Offer{
		offer("a", catalogue.GradeGood, 40000),
		offer("b", catalogue.GradeGood, 40000),
		offer("c", catalogue.GradeGood, 40000, seller("other-seller")),
		offer("d", catalogue.GradeExcellent, 42000),
	}
	first, firstRest, _ := catalogue.BuyBox(all, sellers)

	rng := rand.New(rand.NewSource(4))
	for i := 0; i < 200; i++ {
		shuffled := append([]catalogue.Offer(nil), all...)
		rng.Shuffle(len(shuffled), func(x, y int) { shuffled[x], shuffled[y] = shuffled[y], shuffled[x] })

		w, rest, _ := catalogue.BuyBox(shuffled, sellers)
		if w.ID != first.ID {
			t.Fatalf("shuffle %d put %s in the buy box, not %s", i, w.ID, first.ID)
		}
		for j := range rest {
			if rest[j].ID != firstRest[j].ID {
				t.Fatalf("shuffle %d reordered the rest: %s at %d, want %s", i, rest[j].ID, j, firstRest[j].ID)
			}
		}
	}
}

// TestFromPriceOnlyCountsWhatCanBeBought. A card saying 389 that leads to a
// page where the cheapest is 450 is how people stop trusting a marketplace.
func TestFromPriceOnlyCountsWhatCanBeBought(t *testing.T) {
	all := []catalogue.Offer{
		offer("sold", catalogue.GradeFair, 29900, state(catalogue.Sold)),
		offer("held", catalogue.GradeFair, 30900, state(catalogue.Reserved)),
		offer("live", catalogue.GradeGood, 38900),
		offer("dearer", catalogue.GradeExcellent, 45900),
	}
	cents, currency, ok := catalogue.FromPrice(all)
	if !ok {
		t.Fatal("no from price")
	}
	if cents != 38900 {
		t.Fatalf("from price is %d, and that offer cannot be bought", cents)
	}
	if currency != "EUR" {
		t.Fatalf("currency is %q", currency)
	}

	if _, _, ok := catalogue.FromPrice([]catalogue.Offer{
		offer("x", catalogue.GradeGood, 100, state(catalogue.Sold)),
	}); ok {
		t.Fatal("a product with nothing available still had a from price")
	}
}

func TestGradeCounts(t *testing.T) {
	all := []catalogue.Offer{
		offer("a", catalogue.GradeGood, 40000),
		offer("b", catalogue.GradeGood, 41000),
		offer("c", catalogue.GradeExcellent, 45000),
		offer("d", catalogue.GradeFair, 30000, state(catalogue.Sold)),
	}
	counts := catalogue.GradeCounts(all)
	if counts[catalogue.GradeGood] != 2 || counts[catalogue.GradeExcellent] != 1 {
		t.Fatalf("counts are %v", counts)
	}
	if counts[catalogue.GradeFair] != 0 {
		t.Fatalf("a sold offer was counted in the filter, so the filter leads to an empty page")
	}
}

// TestCheapestPerGrade answers the question customers actually ask, which is
// not "what is the cheapest" but "what does an excellent one cost".
func TestCheapestPerGrade(t *testing.T) {
	all := []catalogue.Offer{
		offer("exc-dear", catalogue.GradeExcellent, 49000),
		offer("exc-cheap", catalogue.GradeExcellent, 45000),
		offer("good", catalogue.GradeGood, 39000),
		offer("good-sold", catalogue.GradeGood, 31000, state(catalogue.Sold)),
	}
	byGrade := catalogue.CheapestPerGrade(all)
	if byGrade[catalogue.GradeExcellent].ID != "exc-cheap" {
		t.Fatalf("cheapest excellent is %s", byGrade[catalogue.GradeExcellent].ID)
	}
	if byGrade[catalogue.GradeGood].ID != "good" {
		t.Fatalf("a sold offer was reported as the cheapest good one")
	}
	if _, present := byGrade[catalogue.GradeFair]; present {
		t.Fatal("a grade with no offers appeared")
	}
}

func TestOfferValidation(t *testing.T) {
	good := offer("a", catalogue.GradeGood, 1000)
	if err := good.Validate(); err != nil {
		t.Fatalf("a good offer was refused: %v", err)
	}
	for name, mutate := range map[string]func(*catalogue.Offer){
		"no id":          func(o *catalogue.Offer) { o.ID = "" },
		"no product":     func(o *catalogue.Offer) { o.ProductID = "" },
		"no seller":      func(o *catalogue.Offer) { o.SellerID = "" },
		"unknown grade":  func(o *catalogue.Offer) { o.Grade = catalogue.GradeUnknown },
		"free":           func(o *catalogue.Offer) { o.PriceCents = 0 },
		"negative price": func(o *catalogue.Offer) { o.PriceCents = -1 },
		"no currency":    func(o *catalogue.Offer) { o.Currency = "" },
		"110% battery":   func(o *catalogue.Offer) { o.BatteryPct = 110 },
	} {
		bad := good
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
