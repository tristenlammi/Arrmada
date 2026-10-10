package requests

import (
	"errors"
	"reflect"
	"testing"
)

// TestCreateSeriesSeasonsOverlap is the overlap rule as a table: what's covered, what's
// left, who follows what, when a row is whole-show, and declined rows.
func TestCreateSeriesSeasonsOverlap(t *testing.T) {
	known := []int{1, 2, 3, 4}
	cases := []struct {
		name   string
		asked  []int
		known  []int
		onDisk map[int]bool
		rows   []seasonRow
		want   seasonPlan
		err    error
	}{
		{name: "fresh show, some seasons", asked: []int{2, 1, 2, 0}, known: known,
			want: seasonPlan{insert: true, seasons: []int{1, 2}}},
		{name: "fresh show, whole", known: known,
			want: seasonPlan{insert: true}},
		{name: "unknown seasons and specials are dropped", asked: []int{0, 3, 9}, known: known,
			want: seasonPlan{insert: true, seasons: []int{3}}},
		{name: "nothing known asked for", asked: []int{0, 9}, known: known, err: ErrNoSuchSeasons},
		{name: "S1-3 on disk, ask S4", asked: []int{4}, known: known, onDisk: map[int]bool{1: true, 2: true, 3: true},
			want: seasonPlan{insert: true, seasons: []int{4}}},
		{name: "S1-3 on disk, whole show asks the rest", known: known, onDisk: map[int]bool{1: true, 2: true, 3: true},
			want: seasonPlan{insert: true, seasons: []int{4}}},
		{name: "everything on disk, no request to follow", asked: []int{1}, known: known, onDisk: map[int]bool{1: true},
			err: ErrAlreadyAvailable},
		{name: "a second user asking [2] follows [2]", asked: []int{2}, known: known,
			rows: []seasonRow{{id: 5, status: StatusPending, seasons: []int{2}, requestedBy: 7}},
			want: seasonPlan{follow: []int64{5}}},
		{name: "asking [2,3] follows [2] and asks for [3]", asked: []int{2, 3}, known: known,
			rows: []seasonRow{{id: 5, status: StatusApproved, seasons: []int{2}, requestedBy: 7}},
			want: seasonPlan{insert: true, seasons: []int{3}, follow: []int64{5}}},
		{name: "a pending whole-show request covers everything", asked: []int{3}, known: known,
			rows: []seasonRow{{id: 5, status: StatusPending, requestedBy: 7}},
			want: seasonPlan{follow: []int64{5}}},
		{name: "a delivered whole-show request covers nothing", asked: []int{4}, known: known,
			rows: []seasonRow{{id: 5, status: StatusApproved, done: true, requestedBy: 7}},
			want: seasonPlan{insert: true, seasons: []int{4}}},
		{name: "whole show again after a delivered whole-show request is asked by list", known: known,
			rows: []seasonRow{{id: 5, status: StatusApproved, done: true, requestedBy: 7}},
			want: seasonPlan{insert: true, seasons: []int{1, 2, 3, 4}}},
		{name: "a declined row with the same ask is re-opened", asked: []int{3}, known: known,
			rows: []seasonRow{{id: 5, status: StatusDeclined, seasons: []int{3}, requestedBy: 7}},
			want: seasonPlan{insert: true, seasons: []int{3}, reopen: 5}},
		{name: "a declined overlapping row's requester hears about the new one", asked: []int{3, 4}, known: known,
			rows: []seasonRow{
				{id: 5, status: StatusDeclined, seasons: []int{3}, requestedBy: 7},
				{id: 6, status: StatusDeclined, seasons: []int{1}, requestedBy: 8},
				{id: 7, status: StatusDeclined, requestedBy: 9},
			},
			want: seasonPlan{insert: true, seasons: []int{3, 4}, declinedBy: []int64{7, 9}}},
		{name: "a declined whole-show row is re-opened by a whole-show ask", known: known,
			rows: []seasonRow{{id: 5, status: StatusDeclined, requestedBy: 7}},
			want: seasonPlan{insert: true, reopen: 5}},
		{name: "no catalogue: whole show follows a whole-show row", rows: []seasonRow{
			{id: 4, status: StatusPending, seasons: []int{1}, requestedBy: 7},
			{id: 5, status: StatusApproved, requestedBy: 8},
		}, want: seasonPlan{follow: []int64{5}}},
		{name: "no catalogue: whole show is stored whole", rows: []seasonRow{{id: 4, status: StatusPending, seasons: []int{1}}},
			want: seasonPlan{insert: true}},
		{name: "no catalogue: a declined whole-show row is re-opened", rows: []seasonRow{{id: 4, status: StatusDeclined, requestedBy: 7}},
			want: seasonPlan{insert: true, reopen: 4}},
		{name: "no catalogue: explicit seasons are taken as asked", asked: []int{5},
			want: seasonPlan{insert: true, seasons: []int{5}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := planSeasons(tc.asked, tc.known, tc.onDisk, tc.rows)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if err != nil {
				return
			}
			// What a new row counts against a limit: the seasons in it, or for a whole-show
			// row every season nobody covers (one with no season list).
			wantUnits := len(tc.want.seasons)
			if tc.want.insert && tc.want.seasons == nil {
				wantUnits = max(len(got.seasons), 1)
				if len(tc.known) > 0 {
					wantUnits = 0
					for _, n := range tc.known {
						if n > 0 && !tc.onDisk[n] {
							wantUnits++
						}
					}
				}
			}
			if got.units != wantUnits {
				t.Errorf("units = %d, want %d", got.units, wantUnits)
			}
			got.units = 0
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("plan = %+v\n want %+v", got, tc.want)
			}
		})
	}
}
