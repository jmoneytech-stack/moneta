package main

import (
	"fmt"
	"time"
)

type readPeriod struct {
	From string
	To   string
}

// resolveReadPeriod accepts either --period YYYY-MM, a complete custom
// --from/--to pair, or no period flags (the current calendar month in the
// host's local timezone). Spend and cashflow share this exact flag contract.
func resolveReadPeriod(periodValue, from, to string, now time.Time) (readPeriod, error) {
	if periodValue != "" && (from != "" || to != "") {
		return readPeriod{}, fmt.Errorf("--period cannot be combined with --from or --to")
	}
	if (from == "") != (to == "") {
		return readPeriod{}, fmt.Errorf("--from and --to must be provided together")
	}
	if from != "" {
		if err := validateCLIDate("from", from); err != nil {
			return readPeriod{}, err
		}
		if err := validateCLIDate("to", to); err != nil {
			return readPeriod{}, err
		}
		if from > to {
			return readPeriod{}, fmt.Errorf("--from must not be after --to")
		}
		return readPeriod{From: from, To: to}, nil
	}

	if periodValue == "" {
		periodValue = now.Format("2006-01")
	}
	month, err := time.Parse("2006-01", periodValue)
	if err != nil || month.Format("2006-01") != periodValue {
		return readPeriod{}, fmt.Errorf("--period must use valid YYYY-MM form, got %q", periodValue)
	}
	first := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, now.Location())
	last := first.AddDate(0, 1, -1)
	return readPeriod{
		From: first.Format("2006-01-02"),
		To:   last.Format("2006-01-02"),
	}, nil
}
