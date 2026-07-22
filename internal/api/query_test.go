package api

import (
	"net/url"
	"testing"
	"time"
)

func TestResolvePeriod(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.FixedZone("local", -7*60*60))
	tests := []struct {
		name    string
		query   url.Values
		want    period
		wantErr bool
	}{
		{"default current local month", nil, period{"2026-07-01", "2026-07-31"}, false},
		{"explicit leap month", url.Values{"period": {"2024-02"}}, period{"2024-02-01", "2024-02-29"}, false},
		{"custom inclusive dates", url.Values{"from": {"2026-06-15"}, "to": {"2026-07-14"}}, period{"2026-06-15", "2026-07-14"}, false},
		{"invalid month", url.Values{"period": {"2026-13"}}, period{}, true},
		{"month and dates conflict", url.Values{"period": {"2026-07"}, "from": {"2026-07-01"}, "to": {"2026-07-31"}}, period{}, true},
		{"partial custom", url.Values{"from": {"2026-07-01"}}, period{}, true},
		{"invalid custom date", url.Values{"from": {"2026-02-30"}, "to": {"2026-03-01"}}, period{}, true},
		{"inverted custom dates", url.Values{"from": {"2026-07-31"}, "to": {"2026-07-01"}}, period{}, true},
		{"duplicate parameter", url.Values{"period": {"2026-06", "2026-07"}}, period{}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolvePeriod(test.query, now)
			if (err != nil) != test.wantErr {
				t.Fatalf("resolvePeriod() error = %v, wantErr %v", err, test.wantErr)
			}
			if got != test.want {
				t.Errorf("resolvePeriod() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestParseLimit(t *testing.T) {
	tests := []struct {
		name      string
		query     url.Values
		wantLimit int
		wantFull  bool
		wantErr   bool
	}{
		{"default", nil, 20, false, false},
		{"custom", url.Values{"limit": {"5"}}, 5, false, false},
		{"full", url.Values{"limit": {"5"}, "full": {"true"}}, 0, true, false},
		{"zero", url.Values{"limit": {"0"}}, 0, false, true},
		{"invalid full", url.Values{"full": {"yes"}}, 0, false, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limit, full, err := parseLimit(test.query)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseLimit() error = %v, wantErr %v", err, test.wantErr)
			}
			if limit != test.wantLimit || full != test.wantFull {
				t.Errorf("parseLimit() = %d, %v, want %d, %v", limit, full, test.wantLimit, test.wantFull)
			}
		})
	}
}
