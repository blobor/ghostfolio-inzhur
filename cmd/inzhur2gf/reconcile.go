package main

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/blobor/ghostfolio-inzhur/ghostfolio"
	"github.com/blobor/ghostfolio-inzhur/internal/mapper"
)

// plan is the work that makes the account in Ghostfolio agree with Inzhur.
type plan struct {
	// create has the activities that Ghostfolio does not have.
	create []ghostfolio.Activity
	// update has the activities whose values are different in Ghostfolio.
	update []change
	// duplicates has the transactions with more than one activity in
	// Ghostfolio.
	duplicates []duplicate
	// extra has the activities of Ghostfolio whose transaction is not in
	// Inzhur.
	extra []ghostfolio.StoredActivity
}

// change is the update of one activity in Ghostfolio.
type change struct {
	id       string
	old, new ghostfolio.Activity
}

// duplicate is one transaction with more than one activity in Ghostfolio.
type duplicate struct {
	comment string
	ids     []string
}

// reconcile compares the activities from Inzhur with the activities that
// Ghostfolio has. The comment of an activity has the Inzhur transaction
// ID, and this ID identifies the activity.
//
// Ghostfolio finds a duplicate only if all values are equal. Inzhur can
// change a transaction after the import, for example its time. Thus the
// transaction ID decides, not the values.
//
// reconcile does not put a transaction with duplicates into the update
// list, because it cannot know which copy to keep. It ignores activities
// that do not come from this program.
func reconcile(desired []ghostfolio.Activity, stored []ghostfolio.StoredActivity) plan {
	known := make(map[string][]ghostfolio.StoredActivity)
	for _, a := range stored {
		if strings.HasPrefix(a.Comment, mapper.CommentPrefix) {
			known[a.Comment] = append(known[a.Comment], a)
		}
	}

	var p plan
	wanted := make(map[string]bool, len(desired))
	for _, a := range desired {
		wanted[a.Comment] = true

		copies := known[a.Comment]
		switch {
		case len(copies) == 0:
			p.create = append(p.create, a)
		case len(copies) > 1:
			d := duplicate{comment: a.Comment}
			for _, c := range copies {
				d.ids = append(d.ids, c.ID)
			}
			p.duplicates = append(p.duplicates, d)
		case !sameValues(a, copies[0].Activity):
			p.update = append(p.update, change{id: copies[0].ID, old: copies[0].Activity, new: a})
		}
	}

	for _, a := range stored {
		if strings.HasPrefix(a.Comment, mapper.CommentPrefix) && !wanted[a.Comment] {
			p.extra = append(p.extra, a)
		}
	}
	return p
}

// sameValues tells if two activities have the same values. Ghostfolio
// keeps the date to the second.
func sameValues(a, b ghostfolio.Activity) bool {
	return a.Type == b.Type &&
		a.Symbol == b.Symbol &&
		a.Date.Truncate(time.Second).Equal(b.Date.Truncate(time.Second)) &&
		math.Abs(a.Quantity-b.Quantity) <= quantityTolerance &&
		math.Abs(a.UnitPrice-b.UnitPrice) <= quantityTolerance &&
		math.Abs(a.Fee-b.Fee) <= quantityTolerance
}

func describe(a ghostfolio.Activity) string {
	return fmt.Sprintf(
		"%s %s %s quantity=%v price=%v fee=%v",
		a.Date.UTC().Format(time.RFC3339),
		a.Type,
		a.Symbol,
		a.Quantity,
		a.UnitPrice,
		a.Fee,
	)
}
