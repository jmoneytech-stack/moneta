package api

import (
	"fmt"
	"net/url"
	"strconv"
	"time"
)

const defaultLimit = 20

type period struct {
	from string
	to   string
}

func validateQueryKeys(query url.Values, allowed ...string) error {
	known := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		known[key] = struct{}{}
	}
	for key := range query {
		if _, ok := known[key]; !ok {
			return fmt.Errorf("unknown query parameter %q", key)
		}
	}
	return nil
}

func queryValue(query url.Values, name string) (string, error) {
	values, ok := query[name]
	if !ok {
		return "", nil
	}
	if len(values) != 1 {
		return "", fmt.Errorf("query parameter %q must appear once", name)
	}
	return values[0], nil
}

func parseLimit(query url.Values) (int, bool, error) {
	limitValue, err := queryValue(query, "limit")
	if err != nil {
		return 0, false, err
	}
	limit := defaultLimit
	if limitValue != "" {
		limit, err = strconv.Atoi(limitValue)
		if err != nil || limit < 1 {
			return 0, false, fmt.Errorf("query parameter %q must be an integer of at least 1", "limit")
		}
	}
	fullValue, err := queryValue(query, "full")
	if err != nil {
		return 0, false, err
	}
	full := false
	if fullValue != "" {
		full, err = strconv.ParseBool(fullValue)
		if err != nil {
			return 0, false, fmt.Errorf("query parameter %q must be true or false", "full")
		}
	}
	if full {
		return 0, true, nil
	}
	return limit, false, nil
}

func validateDate(name, value string) error {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return fmt.Errorf("query parameter %q must be a valid YYYY-MM-DD date", name)
	}
	return nil
}

func resolvePeriod(query url.Values, now time.Time) (period, error) {
	periodValue, err := queryValue(query, "period")
	if err != nil {
		return period{}, err
	}
	from, err := queryValue(query, "from")
	if err != nil {
		return period{}, err
	}
	to, err := queryValue(query, "to")
	if err != nil {
		return period{}, err
	}
	if periodValue != "" && (from != "" || to != "") {
		return period{}, fmt.Errorf("query parameter %q cannot be combined with %q or %q", "period", "from", "to")
	}
	if (from == "") != (to == "") {
		return period{}, fmt.Errorf("query parameters %q and %q must be provided together", "from", "to")
	}
	if from != "" {
		if err := validateDate("from", from); err != nil {
			return period{}, err
		}
		if err := validateDate("to", to); err != nil {
			return period{}, err
		}
		if from > to {
			return period{}, fmt.Errorf("query parameter %q must not be after %q", "from", "to")
		}
		return period{from: from, to: to}, nil
	}

	if periodValue == "" {
		periodValue = now.Format("2006-01")
	}
	month, err := time.Parse("2006-01", periodValue)
	if err != nil || month.Format("2006-01") != periodValue {
		return period{}, fmt.Errorf("query parameter %q must use valid YYYY-MM form", "period")
	}
	first := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, now.Location())
	last := first.AddDate(0, 1, -1)
	return period{from: first.Format("2006-01-02"), to: last.Format("2006-01-02")}, nil
}
