package main

import (
	"slices"
	"testing"
	"time"

	"github.com/blobor/ghostfolio-inzhur/ghostfolio"
)

func TestReconcile(t *testing.T) {
	t.Parallel()

	date := time.Date(2026, 1, 5, 2, 34, 38, 0, time.UTC)
	activity := func(comment string, when time.Time) ghostfolio.Activity {
		return ghostfolio.Activity{
			AccountID:  "account-1",
			Comment:    comment,
			Currency:   "UAH",
			DataSource: ghostfolio.DataSourceManual,
			Date:       when,
			Quantity:   10,
			Symbol:     "GF_INZHUR-REIT",
			Type:       ghostfolio.TypeBuy,
			UnitPrice:  11.6,
		}
	}
	stored := func(id, comment string, when time.Time) ghostfolio.StoredActivity {
		return ghostfolio.StoredActivity{ID: id, Activity: activity(comment, when)}
	}
	desired := []ghostfolio.Activity{activity("inzhur:1", date)}

	tests := []struct {
		name           string
		stored         []ghostfolio.StoredActivity
		wantCreate     []string
		wantUpdate     []string
		wantDuplicates []string
		wantExtra      []string
	}{
		{
			name:       "empty account",
			wantCreate: []string{"inzhur:1"},
		},
		{
			name:   "equal activity",
			stored: []ghostfolio.StoredActivity{stored("gf-1", "inzhur:1", date)},
		},
		{
			name:   "equal activity with a fraction of a second",
			stored: []ghostfolio.StoredActivity{stored("gf-1", "inzhur:1", date.Add(300*time.Millisecond))},
		},
		{
			name:       "different time",
			stored:     []ghostfolio.StoredActivity{stored("gf-1", "inzhur:1", date.Add(19*time.Hour))},
			wantUpdate: []string{"gf-1"},
		},
		{
			name: "two copies, one is equal",
			stored: []ghostfolio.StoredActivity{
				stored("gf-1", "inzhur:1", date),
				stored("gf-2", "inzhur:1", date.Add(19*time.Hour)),
			},
			wantDuplicates: []string{"gf-1,gf-2"},
		},
		{
			name: "transaction that is not in Inzhur",
			stored: []ghostfolio.StoredActivity{
				stored("gf-1", "inzhur:1", date),
				stored("gf-2", "inzhur:2", date),
			},
			wantExtra: []string{"gf-2"},
		},
		{
			name: "activity of the user",
			stored: []ghostfolio.StoredActivity{
				stored("gf-1", "inzhur:1", date),
				stored("gf-2", "made by hand", date),
				stored("gf-3", "", date),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := reconcile(desired, tt.stored)

			var create, update, duplicates, extra []string
			for _, a := range p.create {
				create = append(create, a.Comment)
			}
			for _, c := range p.update {
				update = append(update, c.id)
				if c.new != desired[0] {
					t.Errorf("update has the values %+v, want %+v", c.new, desired[0])
				}
			}
			for _, d := range p.duplicates {
				duplicates = append(duplicates, d.ids[0]+","+d.ids[1])
			}
			for _, a := range p.extra {
				extra = append(extra, a.ID)
			}

			if !slices.Equal(create, tt.wantCreate) {
				t.Errorf("create = %v, want %v", create, tt.wantCreate)
			}
			if !slices.Equal(update, tt.wantUpdate) {
				t.Errorf("update = %v, want %v", update, tt.wantUpdate)
			}
			if !slices.Equal(duplicates, tt.wantDuplicates) {
				t.Errorf("duplicates = %v, want %v", duplicates, tt.wantDuplicates)
			}
			if !slices.Equal(extra, tt.wantExtra) {
				t.Errorf("extra = %v, want %v", extra, tt.wantExtra)
			}
		})
	}
}
