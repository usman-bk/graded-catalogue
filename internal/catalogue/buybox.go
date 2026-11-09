package catalogue

import "sort"

// GradePremiumBasis is how much better a grade is worth, as a fraction of
// the offer's own price.
//
// Comparing a fair unit at 380 euros with an excellent one at 400 by price
// alone picks the fair one every time, and the customer who receives a
// scratched phone did not knowingly save 20 euros: they were shown the
// cheapest thing and assumed it was the best deal. So each step of grade is
// worth a percentage of the price, and the comparison is made on what the
// offer is worth rather than on what it costs.
//
// 6% a step is not derived from anything. It is a number somebody chose,
// which is the honest description of every weight in a ranking like this,
// and the reason it is one named constant rather than scattered through a
// comparison function is so that changing it is one edit and one test.
const GradePremiumBasis = 0.06

// SellerPenaltyCents is added to the score for a seller nobody has rated.
//
// A new seller with no history is not a bad seller, and putting them at the
// top of the buy box on price alone is a bet made with the customer's money.
// A flat penalty rather than a multiplier, because a percentage would punish
// an unrated seller far more on an expensive item, which is backwards: the
// risk is the same transaction either way.
const SellerPenaltyCents = 1500

// MinRatings is how many ratings a seller needs before their score counts.
const MinRatings = 10

// Score is what the buy box ranks on, in cents. Lower wins.
//
// It is a price with adjustments, deliberately, rather than an abstract
// number out of 100. A number in cents can be explained to a seller who
// asks why they lost the buy box, and a seller who cannot be given that
// answer assumes the marketplace is rigged.
func Score(o Offer, s Seller) int64 {
	price := float64(o.PriceCents)

	// Each step below excellent makes the offer worth less than its price.
	steps := float64(GradeExcellent - o.Grade)
	score := price + price*GradePremiumBasis*steps

	// Battery is a real difference between two units of the same grade and
	// it is only meaningful where there is a battery. Below 85% is the line
	// where a replacement starts being a cost the buyer takes on.
	if o.BatteryPct > 0 && o.BatteryPct < 85 {
		score += price * 0.04
	}

	if s.RatingCount < MinRatings {
		score += SellerPenaltyCents
	} else if s.Rating < 4.0 {
		score += SellerPenaltyCents
	}

	// A longer warranty is worth something, capped so that a seller cannot
	// win the buy box by claiming 120 months.
	months := o.WarrantyMonths
	if months > 24 {
		months = 24
	}
	score -= float64(months) * 100

	return int64(score)
}

// BuyBox picks the offer to show, and reports the others in order.
//
// Only available offers are considered. A reserved offer is in somebody's
// basket and may come back, and showing it would mean the customer meets
// "no longer available" at checkout, which is the worst place to meet it.
func BuyBox(offers []Offer, sellers map[string]Seller) (winner Offer, rest []Offer, ok bool) {
	var live []Offer
	for _, o := range offers {
		if o.State == Available {
			live = append(live, o)
		}
	}
	if len(live) == 0 {
		return Offer{}, nil, false
	}

	sort.SliceStable(live, func(i, j int) bool {
		si := Score(live[i], sellers[live[i].SellerID])
		sj := Score(live[j], sellers[live[j].SellerID])
		if si != sj {
			return si < sj
		}
		// Ties broken by the offer id, so the buy box does not move between
		// two page loads for no reason. A ranking that is unstable looks
		// broken to a customer and looks rigged to a seller.
		return live[i].ID < live[j].ID
	})

	return live[0], live[1:], true
}

// FromPrice is the "from" figure on a product card: the cheapest price a
// customer can actually pay right now.
//
// Cheapest available, not cheapest listed. A card advertising 389 euros that
// leads to a page where the cheapest is 450 is the single most effective way
// to make people stop trusting a marketplace, and it happens by caching this
// number and forgetting that the offer behind it sold.
func FromPrice(offers []Offer) (cents int64, currency string, ok bool) {
	for _, o := range offers {
		if o.State != Available {
			continue
		}
		if !ok || o.PriceCents < cents {
			cents, currency, ok = o.PriceCents, o.Currency, true
		}
	}
	return cents, currency, ok
}

// GradeCounts is how many available offers sit at each grade, for the
// filter counts beside a product.
func GradeCounts(offers []Offer) map[Grade]int {
	out := map[Grade]int{}
	for _, o := range offers {
		if o.State == Available {
			out[o.Grade]++
		}
	}
	return out
}

// CheapestPerGrade answers the question a customer actually asks, which is
// not "what is the cheapest" but "what does an excellent one cost".
func CheapestPerGrade(offers []Offer) map[Grade]Offer {
	out := map[Grade]Offer{}
	for _, o := range offers {
		if o.State != Available {
			continue
		}
		cur, seen := out[o.Grade]
		if !seen || o.PriceCents < cur.PriceCents ||
			(o.PriceCents == cur.PriceCents && o.ID < cur.ID) {
			out[o.Grade] = o
		}
	}
	return out
}
