// Package recurring implements pure recurring-transaction detection.
package recurring

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

const dateLayout = time.DateOnly

var (
	tailHashDigits = regexp.MustCompile(`#\d+$`)
	tailLongDigits = regexp.MustCompile(`(?:^|\s)\d{4,}$`)
)

// Candidate is one transaction available to recurring detection. The loader
// supplies provider item provenance for later partial-mode persistence; the
// pure detector does not mutate or otherwise consume provider state.
type Candidate struct {
	TransactionID   int64
	EntityID        int64
	ProviderItemID  *int64
	Date            string
	AmountCents     int64
	MerchantDisplay string
	MerchantNorm    string
	MerchantRaw     string
	Category        string
	Status          string
	Excluded        bool
	IsTransfer      bool
}

// Identity is the durable detected-series identity introduced by migration
// 000005. Overflow identities are returned so later persistence does not treat
// a skipped pathological partition as unseen.
type Identity struct {
	EntityID   int64
	DetectKey  string
	AmountSign int
}

// Series is one fully recomputed recurring series. Amounts retain the signed
// transaction convention. MemberTransactionIDs contains only the selected
// cadence subsequence, ordered by date then transaction id.
type Series struct {
	EntityID             int64
	DetectKey            string
	AmountSign           int
	Name                 string
	Kind                 string
	Cadence              string
	ExpectedCents        int64
	NextExpectedDate     string
	IsActive             bool
	MissCount            int
	LastMatchedDate      string
	LastMatchedCents     int64
	ScheduleAnchorDay    int
	MemberTransactionIDs []int64
}

// Result is the complete output of one pure detect pass.
type Result struct {
	Series             []Series
	SkippedOverflow    int
	OverflowIdentities []Identity
}

// DetectKeyV1 applies the frozen v1 recurring merchant identity algorithm.
func DetectKeyV1(input string) string {
	s := strings.ToLower(strings.Join(strings.Fields(input), " "))
	for {
		before := s
		if location := tailHashDigits.FindStringIndex(s); location != nil {
			s = strings.Join(strings.Fields(s[:location[0]]), " ")
		} else if location := tailLongDigits.FindStringIndex(s); location != nil {
			s = strings.Join(strings.Fields(s[:location[0]]), " ")
		}
		if s == before {
			return s
		}
	}
}

// Lookback returns the exact inclusive v1 recurring-detection date range.
func Lookback(asOf string) (string, string, error) {
	end, err := parseDate(asOf)
	if err != nil {
		return "", "", fmt.Errorf("parse recurring as-of date: %w", err)
	}
	start := time.Date(end.Year(), end.Month()-14, 1, 0, 0, 0, 0, time.UTC)
	return start.Format(dateLayout), end.Format(dateLayout), nil
}

// Detect computes recurring series without performing database writes.
func Detect(rows []Candidate, asOf string) (Result, error) {
	var result Result
	startText, endText, err := Lookback(asOf)
	if err != nil {
		return result, err
	}
	start, _ := parseDate(startText)
	end, _ := parseDate(endText)

	partitions := make(map[Identity][]occurrence)
	for _, row := range rows {
		if row.Status != "posted" || row.Excluded || row.IsTransfer || row.AmountCents == 0 {
			continue
		}
		date, err := parseDate(row.Date)
		if err != nil || date.Before(start) || date.After(end) {
			continue
		}
		merchant := row.MerchantDisplay
		if merchant == "" {
			merchant = row.MerchantNorm
		}
		detectKey := DetectKeyV1(merchant)
		if detectKey == "" {
			continue
		}
		amountSign := 1
		if row.AmountCents < 0 {
			amountSign = -1
		}
		identity := Identity{
			EntityID:   row.EntityID,
			DetectKey:  detectKey,
			AmountSign: amountSign,
		}
		partitions[identity] = append(partitions[identity], occurrence{
			Candidate: row,
			date:      date,
		})
	}

	identities := make([]Identity, 0, len(partitions))
	for identity := range partitions {
		identities = append(identities, identity)
	}
	sort.Slice(identities, func(i, j int) bool {
		if identities[i].EntityID != identities[j].EntityID {
			return identities[i].EntityID < identities[j].EntityID
		}
		if identities[i].DetectKey != identities[j].DetectKey {
			return identities[i].DetectKey < identities[j].DetectKey
		}
		return identities[i].AmountSign < identities[j].AmountSign
	})

	for _, identity := range identities {
		series, emitted, overflow := detectPartition(identity, partitions[identity], end)
		if overflow {
			result.SkippedOverflow++
			result.OverflowIdentities = append(result.OverflowIdentities, identity)
			continue
		}
		if emitted {
			result.Series = append(result.Series, series)
		}
	}
	return result, nil
}

type occurrence struct {
	Candidate
	date time.Time
}

type cadenceSpec struct {
	name       string
	minimum    int
	maximum    int
	stepDays   int
	stepMonths int
	graceDays  int
	order      int
}

var cadenceSpecs = []cadenceSpec{
	{name: "weekly", minimum: 6, maximum: 8, stepDays: 7, graceDays: 1, order: 0},
	{name: "biweekly", minimum: 13, maximum: 15, stepDays: 14, graceDays: 1, order: 1},
	{name: "monthly", minimum: 27, maximum: 33, stepMonths: 1, graceDays: 3, order: 2},
	{name: "quarterly", minimum: 85, maximum: 95, stepMonths: 3, graceDays: 3, order: 3},
}

type cadenceMatch struct {
	spec    cadenceSpec
	members []occurrence
	latest  bool
}

func detectPartition(
	identity Identity,
	partition []occurrence,
	asOf time.Time,
) (Series, bool, bool) {
	byAmount := append([]occurrence(nil), partition...)
	sort.Slice(byAmount, func(i, j int) bool {
		if byAmount[i].AmountCents != byAmount[j].AmountCents {
			return byAmount[i].AmountCents < byAmount[j].AmountCents
		}
		if !byAmount[i].date.Equal(byAmount[j].date) {
			return byAmount[i].date.Before(byAmount[j].date)
		}
		return byAmount[i].TransactionID < byAmount[j].TransactionID
	})

	seed := lowerMedian(byAmount).AmountCents
	tolerance, ok := amountTolerance(seed)
	if !ok {
		return Series{}, false, true
	}
	kept := make([]occurrence, 0, len(byAmount))
	for _, candidate := range byAmount {
		distance, ok := amountDistance(candidate.AmountCents, seed)
		if !ok {
			return Series{}, false, true
		}
		if distance <= tolerance {
			kept = append(kept, candidate)
		}
	}
	if len(kept) < 3 {
		return Series{}, false, false
	}
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].AmountCents != kept[j].AmountCents {
			return kept[i].AmountCents < kept[j].AmountCents
		}
		if !kept[i].date.Equal(kept[j].date) {
			return kept[i].date.Before(kept[j].date)
		}
		return kept[i].TransactionID < kept[j].TransactionID
	})
	expected := lowerMedian(kept).AmountCents
	expectedMagnitude, ok := checkedAbs(expected)
	if !ok {
		return Series{}, false, true
	}
	if expectedMagnitude < 500 {
		return Series{}, false, false
	}

	sort.Slice(kept, func(i, j int) bool {
		if !kept[i].date.Equal(kept[j].date) {
			return kept[i].date.Before(kept[j].date)
		}
		return kept[i].TransactionID < kept[j].TransactionID
	})
	match, ok := bestCadenceMatch(kept)
	if !ok {
		return Series{}, false, false
	}
	kind, overflow := seriesKind(identity.AmountSign, match.members)
	if overflow {
		return Series{}, false, true
	}
	anchor := scheduleAnchorDay(match.members)
	next, misses := nextOpenStep(match.members[len(match.members)-1].date, match.spec, anchor, asOf)
	last := match.members[len(match.members)-1]
	memberIDs := make([]int64, 0, len(match.members))
	for _, member := range match.members {
		memberIDs = append(memberIDs, member.TransactionID)
	}

	return Series{
		EntityID:             identity.EntityID,
		DetectKey:            identity.DetectKey,
		AmountSign:           identity.AmountSign,
		Name:                 seriesName(match.members),
		Kind:                 kind,
		Cadence:              match.spec.name,
		ExpectedCents:        expected,
		NextExpectedDate:     next.Format(dateLayout),
		IsActive:             misses < 2,
		MissCount:            misses,
		LastMatchedDate:      last.Date,
		LastMatchedCents:     last.AmountCents,
		ScheduleAnchorDay:    anchor,
		MemberTransactionIDs: memberIDs,
	}, true, false
}

func lowerMedian(rows []occurrence) occurrence {
	return rows[(len(rows)-1)/2]
}

func amountTolerance(expected int64) (int64, bool) {
	magnitude, ok := checkedAbs(expected)
	if !ok || magnitude > math.MaxInt64/5 {
		return 0, false
	}
	tolerance := magnitude * 5 / 100
	if tolerance < 100 {
		tolerance = 100
	}
	return tolerance, true
}

func checkedAbs(value int64) (int64, bool) {
	if value == math.MinInt64 {
		return 0, false
	}
	if value < 0 {
		return -value, true
	}
	return value, true
}

func amountDistance(left, right int64) (int64, bool) {
	var difference int64
	if right > 0 && left < math.MinInt64+right {
		return 0, false
	}
	if right < 0 && left > math.MaxInt64+right {
		return 0, false
	}
	difference = left - right
	return checkedAbs(difference)
}

func checkedAdd(left, right int64) (int64, bool) {
	if right > 0 && left > math.MaxInt64-right {
		return 0, false
	}
	if right < 0 && left < math.MinInt64-right {
		return 0, false
	}
	return left + right, true
}

func bestCadenceMatch(kept []occurrence) (cadenceMatch, bool) {
	var best cadenceMatch
	found := false
	for _, spec := range cadenceSpecs {
		chains := make([][]int, len(kept))
		for end := range kept {
			chains[end] = []int{end}
			for previous := 0; previous < end; previous++ {
				gap := calendarDaysBetween(kept[previous].date, kept[end].date)
				if gap < spec.minimum || gap > spec.maximum {
					continue
				}
				candidate := append(append([]int(nil), chains[previous]...), end)
				if betterChain(candidate, chains[end], kept) {
					chains[end] = candidate
				}
			}
			if len(chains[end]) < 3 {
				continue
			}
			includesLatest := end == len(kept)-1
			minimumCoverage := (len(kept) + 1) / 2
			if !includesLatest && len(chains[end]) < minimumCoverage {
				continue
			}
			members := make([]occurrence, 0, len(chains[end]))
			for _, index := range chains[end] {
				members = append(members, kept[index])
			}
			candidate := cadenceMatch{spec: spec, members: members, latest: includesLatest}
			if !found || betterCadence(candidate, best) {
				best = candidate
				found = true
			}
		}
	}
	return best, found
}

func betterChain(candidate, current []int, kept []occurrence) bool {
	if len(candidate) != len(current) {
		return len(candidate) > len(current)
	}
	candidateFirst := kept[candidate[0]]
	currentFirst := kept[current[0]]
	if !candidateFirst.date.Equal(currentFirst.date) {
		return candidateFirst.date.After(currentFirst.date)
	}
	for index := range candidate {
		candidateID := kept[candidate[index]].TransactionID
		currentID := kept[current[index]].TransactionID
		if candidateID != currentID {
			return candidateID < currentID
		}
	}
	return false
}

func betterCadence(candidate, current cadenceMatch) bool {
	if candidate.latest != current.latest {
		return candidate.latest
	}
	candidateLast := candidate.members[len(candidate.members)-1]
	currentLast := current.members[len(current.members)-1]
	if !candidateLast.date.Equal(currentLast.date) {
		return candidateLast.date.After(currentLast.date)
	}
	if len(candidate.members) != len(current.members) {
		return len(candidate.members) > len(current.members)
	}
	if !candidate.members[0].date.Equal(current.members[0].date) {
		return candidate.members[0].date.After(current.members[0].date)
	}
	if candidate.spec.order != current.spec.order {
		return candidate.spec.order < current.spec.order
	}
	for index := range candidate.members {
		if candidate.members[index].TransactionID != current.members[index].TransactionID {
			return candidate.members[index].TransactionID < current.members[index].TransactionID
		}
	}
	return false
}

func seriesKind(amountSign int, members []occurrence) (string, bool) {
	if amountSign > 0 {
		return "income", false
	}
	type categoryAggregate struct {
		count int
		sum   int64
	}
	categories := make(map[string]categoryAggregate)
	for _, member := range members {
		magnitude, ok := checkedAbs(member.AmountCents)
		if !ok {
			return "", true
		}
		aggregate := categories[member.Category]
		aggregate.count++
		aggregate.sum, ok = checkedAdd(aggregate.sum, magnitude)
		if !ok {
			return "", true
		}
		categories[member.Category] = aggregate
	}
	dominant := ""
	var dominantAggregate categoryAggregate
	set := false
	for name, aggregate := range categories {
		if !set || aggregate.count > dominantAggregate.count ||
			(aggregate.count == dominantAggregate.count && aggregate.sum > dominantAggregate.sum) ||
			(aggregate.count == dominantAggregate.count && aggregate.sum == dominantAggregate.sum && name < dominant) {
			dominant = name
			dominantAggregate = aggregate
			set = true
		}
	}
	if dominant == "Rent and Utilities" {
		return "bill", false
	}
	return "subscription", false
}

func seriesName(members []occurrence) string {
	for index := len(members) - 1; index >= 0; index-- {
		if members[index].MerchantDisplay != "" {
			return members[index].MerchantDisplay
		}
	}
	return members[len(members)-1].MerchantRaw
}

func scheduleAnchorDay(members []occurrence) int {
	counts := make(map[int]int)
	modeDay := 0
	modeCount := 0
	maximumDay := 0
	hasEndOfMonth := false
	for _, member := range members {
		day := member.date.Day()
		counts[day]++
		if counts[day] > modeCount || counts[day] == modeCount && day > modeDay {
			modeDay = day
			modeCount = counts[day]
		}
		if day > maximumDay {
			maximumDay = day
		}
		if day == daysInMonth(member.date.Year(), member.date.Month()) {
			hasEndOfMonth = true
		}
	}
	if maximumDay >= 28 && hasEndOfMonth {
		return maximumDay
	}
	return modeDay
}

func nextOpenStep(
	lastMatched time.Time,
	spec cadenceSpec,
	anchor int,
	asOf time.Time,
) (time.Time, int) {
	next := advanceSchedule(lastMatched, spec, anchor)
	misses := 0
	for asOf.After(next.AddDate(0, 0, spec.graceDays)) {
		misses++
		next = advanceSchedule(next, spec, anchor)
	}
	return next, misses
}

func advanceSchedule(cursor time.Time, spec cadenceSpec, anchor int) time.Time {
	if spec.stepDays != 0 {
		return cursor.AddDate(0, 0, spec.stepDays)
	}
	targetMonth := time.Date(cursor.Year(), cursor.Month()+time.Month(spec.stepMonths), 1, 0, 0, 0, 0, time.UTC)
	day := min(anchor, daysInMonth(targetMonth.Year(), targetMonth.Month()))
	return time.Date(targetMonth.Year(), targetMonth.Month(), day, 0, 0, 0, 0, time.UTC)
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func calendarDaysBetween(start, end time.Time) int {
	return int(end.Sub(start) / (24 * time.Hour))
}

func parseDate(value string) (time.Time, error) {
	date, err := time.Parse(dateLayout, value)
	if err != nil {
		return time.Time{}, err
	}
	return date, nil
}
