// Command cataloguectl loads a product's offers and shows what the buy box
// would do with them, including what happens as units are taken.
//
// A command rather than a service, because the interesting part of this
// repository is the ranking and the reservation, and both are easier to
// look at from a terminal than through an endpoint.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usmanvirk/graded-catalogue/internal/catalogue"
	"github.com/usmanvirk/graded-catalogue/internal/store/postgres"
)

func main() {
	var (
		dsn     = flag.String("database-url", os.Getenv("DATABASE_URL"), "Postgres DSN")
		product = flag.String("product", "p1", "product to show")
		seed    = flag.Int("seed", 0, "create this many offers for the product first")
		take    = flag.Int("take", 0, "reserve this many of the top offers, to show the buy box moving")
	)
	flag.Parse()

	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "database-url is required")
		os.Exit(2)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		fail(err)
	}
	defer pool.Close()

	store := postgres.New(pool)
	if err := store.Migrate(ctx); err != nil {
		fail(err)
	}
	if *seed > 0 {
		if err := seedOffers(ctx, store, *product, *seed); err != nil {
			fail(err)
		}
	}

	for round := 0; round <= *take; round++ {
		all, err := store.ByProduct(ctx, *product)
		if err != nil {
			fail(err)
		}
		if len(all) == 0 {
			fmt.Printf("no offers for %s. Pass -seed to create some.\n", *product)
			return
		}
		ids := make([]string, 0, len(all))
		for _, o := range all {
			ids = append(ids, o.SellerID)
		}
		sellers, err := store.Sellers(ctx, ids)
		if err != nil {
			fail(err)
		}

		if round > 0 {
			fmt.Printf("\nafter %d taken:\n", round)
		}
		show(all, sellers)

		if round < *take {
			winner, _, ok := catalogue.BuyBox(all, sellers)
			if !ok {
				fmt.Println("\nnothing left to take")
				return
			}
			if _, err := store.Reserve(ctx, winner.ID, fmt.Sprintf("basket-%d", round), 15*time.Minute); err != nil {
				fail(err)
			}
		}
	}
}

func show(all []catalogue.Offer, sellers map[string]catalogue.Seller) {
	winner, rest, ok := catalogue.BuyBox(all, sellers)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "\t offer\tgrade\tprice\tbattery\tseller\tscore\tstate")
	if ok {
		line(w, "buy box", winner, sellers[winner.SellerID])
		for _, o := range rest {
			line(w, "", o, sellers[o.SellerID])
		}
	}
	for _, o := range all {
		if o.State != catalogue.Available {
			line(w, "", o, sellers[o.SellerID])
		}
	}
	w.Flush()

	if cents, cur, ok := catalogue.FromPrice(all); ok {
		fmt.Printf("\nfrom %s %.2f\n", cur, float64(cents)/100)
	} else {
		fmt.Println("\nnothing available")
	}
	for _, g := range []catalogue.Grade{catalogue.GradeExcellent, catalogue.GradeGood, catalogue.GradeFair} {
		if o, ok := catalogue.CheapestPerGrade(all)[g]; ok {
			fmt.Printf("cheapest %-9s %s %.2f\n", g, o.Currency, float64(o.PriceCents)/100)
		}
	}
}

func line(w *tabwriter.Writer, label string, o catalogue.Offer, s catalogue.Seller) {
	battery := "n/a"
	if o.BatteryPct > 0 {
		battery = fmt.Sprintf("%d%%", o.BatteryPct)
	}
	rating := fmt.Sprintf("%.1f (%d)", s.Rating, s.RatingCount)
	if s.RatingCount == 0 {
		rating = "unrated"
	}
	fmt.Fprintf(w, "%s\t %s\t%s\t%.2f\t%s\t%s\t%d\t%s\n",
		label, o.ID, o.Grade, float64(o.PriceCents)/100, battery, rating,
		catalogue.Score(o, s), o.State)
}

func seedOffers(ctx context.Context, store *postgres.Store, productID string, n int) error {
	if err := store.PutProduct(ctx, catalogue.Product{
		ID: productID, Title: "iPhone 13 Pro 256 GB", Brand: "Apple",
		Model: "iPhone 13 Pro", Storage: "256 GB", Colour: "Graphite",
	}); err != nil {
		return err
	}
	for _, s := range []catalogue.Seller{
		{ID: "s1", Name: "Renewed GmbH", Rating: 4.7, RatingCount: 2400, DispatchDays: 1},
		{ID: "s2", Name: "CircuitSecond BV", Rating: 4.3, RatingCount: 640, DispatchDays: 2},
		{ID: "s3", Name: "New Trader", Rating: 0, RatingCount: 0, DispatchDays: 3},
	} {
		if err := store.PutSeller(ctx, s); err != nil {
			return err
		}
	}

	grades := []catalogue.Grade{catalogue.GradeExcellent, catalogue.GradeGood, catalogue.GradeFair}
	sellers := []string{"s1", "s2", "s3"}
	for i := 0; i < n; i++ {
		g := grades[i%len(grades)]
		price := int64(46000 - i*700 - int(g)*1500)
		if err := store.PutOffer(ctx, catalogue.Offer{
			ID:             fmt.Sprintf("%s-o%02d", productID, i),
			ProductID:      productID,
			SellerID:       sellers[i%len(sellers)],
			Grade:          g,
			PriceCents:     price,
			Currency:       "EUR",
			BatteryPct:     78 + (i*7)%22,
			WarrantyMonths: []int{6, 12, 24}[i%3],
		}); err != nil {
			return err
		}
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "cataloguectl:", err)
	os.Exit(1)
}
