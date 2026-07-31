package recurring

import (
	"math"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestDetectRecurringFindsMonthlySubscription(t *testing.T) {
	rows := []Candidate{
		row(1, 1, "2026-03-01", -1500, "Streambox Example", "Entertainment"),
		row(2, 1, "2026-04-01", -1500, "Streambox Example", "Entertainment"),
		row(3, 1, "2026-05-01", -1500, "Streambox Example", "Entertainment"),
		row(4, 1, "2026-06-01", -1500, "Streambox Example", "Entertainment"),
	}

	result := detectAt(t, rows, "2026-06-15")
	if len(result.Series) != 1 {
		t.Fatalf("Detect() series = %d, want 1", len(result.Series))
	}
	series := result.Series[0]
	if series.EntityID != 1 || series.DetectKey != "streambox example" ||
		series.AmountSign != -1 || series.Name != "Streambox Example" ||
		series.Kind != "subscription" || series.Cadence != "monthly" ||
		series.ExpectedCents != -1500 || series.NextExpectedDate != "2026-07-01" ||
		!series.IsActive || series.MissCount != 0 ||
		series.LastMatchedDate != "2026-06-01" || series.LastMatchedCents != -1500 ||
		series.ScheduleAnchorDay != 1 {
		t.Errorf("Detect() series = %+v", series)
	}
	if !reflect.DeepEqual(series.MemberTransactionIDs, []int64{1, 2, 3, 4}) {
		t.Errorf("member ids = %v, want [1 2 3 4]", series.MemberTransactionIDs)
	}
}

func TestDetectRecurringIncomeAndRentBillKind(t *testing.T) {
	rows := []Candidate{
		row(1, 1, "2026-04-05", 250000, "Payroll Example", "Income"),
		row(2, 1, "2026-05-05", 250000, "Payroll Example", "Income"),
		row(3, 1, "2026-06-05", 250000, "Payroll Example", "Income"),
		row(4, 1, "2026-04-01", -180000, "Property Example", "Rent and Utilities"),
		row(5, 1, "2026-05-01", -180000, "Property Example", "Rent and Utilities"),
		row(6, 1, "2026-06-01", -180000, "Property Example", "Rent and Utilities"),
	}

	result := detectAt(t, rows, "2026-06-15")
	byKey := seriesByKey(result.Series)
	if byKey["payroll example"].Kind != "income" || byKey["payroll example"].AmountSign != 1 {
		t.Errorf("income series = %+v", byKey["payroll example"])
	}
	if byKey["property example"].Kind != "bill" || byKey["property example"].AmountSign != -1 {
		t.Errorf("rent series = %+v", byKey["property example"])
	}
}

func TestDetectRecurringRejectsIrregularSparseTinyAndLowCoverage(t *testing.T) {
	rows := []Candidate{
		row(1, 1, "2026-01-01", -1200, "Irregular Example", "Entertainment"),
		row(2, 1, "2026-01-11", -1200, "Irregular Example", "Entertainment"),
		row(3, 1, "2026-02-20", -1200, "Irregular Example", "Entertainment"),
		row(4, 1, "2026-01-01", -1200, "Sparse Example", "Entertainment"),
		row(5, 1, "2026-02-01", -1200, "Sparse Example", "Entertainment"),
		row(6, 1, "2026-01-01", -499, "Tiny Example", "Entertainment"),
		row(7, 1, "2026-02-01", -499, "Tiny Example", "Entertainment"),
		row(8, 1, "2026-03-01", -499, "Tiny Example", "Entertainment"),
		row(9, 1, "2026-01-01", -1200, "Low Coverage Example", "Entertainment"),
		row(10, 1, "2026-02-01", -1200, "Low Coverage Example", "Entertainment"),
		row(11, 1, "2026-03-01", -1200, "Low Coverage Example", "Entertainment"),
		row(12, 1, "2026-04-10", -1200, "Low Coverage Example", "Entertainment"),
		row(13, 1, "2026-05-20", -1200, "Low Coverage Example", "Entertainment"),
		row(14, 1, "2026-06-25", -1200, "Low Coverage Example", "Entertainment"),
		row(15, 1, "2026-07-30", -1200, "Low Coverage Example", "Entertainment"),
	}

	result := detectAt(t, rows, "2026-07-30")
	if len(result.Series) != 0 || result.SkippedOverflow != 0 {
		t.Errorf("Detect() = %+v, want no series and no overflow", result)
	}
}

func TestDetectRecurringSkipsPendingExcludedTransferEmptyKey(t *testing.T) {
	var rows []Candidate
	id := int64(1)
	addMonthly := func(name string, mutate func(*Candidate)) {
		t.Helper()
		for _, date := range []string{"2026-04-01", "2026-05-01", "2026-06-01"} {
			candidate := row(id, 1, date, -1200, name, "Entertainment")
			mutate(&candidate)
			rows = append(rows, candidate)
			id++
		}
	}
	addMonthly("Pending Example", func(candidate *Candidate) { candidate.Status = "pending" })
	addMonthly("Excluded Example", func(candidate *Candidate) { candidate.Excluded = true })
	addMonthly("Transfer Example", func(candidate *Candidate) { candidate.IsTransfer = true })
	addMonthly("", func(candidate *Candidate) {
		candidate.MerchantNorm = ""
		candidate.MerchantRaw = ""
	})
	addMonthly("Eligible Example", func(*Candidate) {})

	result := detectAt(t, rows, "2026-06-15")
	if len(result.Series) != 1 || result.Series[0].DetectKey != "eligible example" {
		t.Errorf("Detect() series = %+v, want eligible example only", result.Series)
	}
}

func TestDetectRecurringCadenceWindows(t *testing.T) {
	var rows []Candidate
	id := int64(1)
	fixtures := []struct {
		name  string
		dates []string
	}{
		{"Weekly Example", []string{"2026-05-01", "2026-05-07", "2026-05-15", "2026-05-22"}},
		{"Biweekly Example", []string{"2026-04-01", "2026-04-14", "2026-04-29", "2026-05-13"}},
		{"Monthly Example", []string{"2026-01-01", "2026-01-28", "2026-03-02", "2026-04-01"}},
		{"Quarterly Example", []string{"2025-07-01", "2025-09-24", "2025-12-28", "2026-03-28", "2026-07-01"}},
		{"Yearly Example", []string{"2024-07-01", "2025-07-01", "2026-07-01"}},
	}
	for _, fixture := range fixtures {
		for _, date := range fixture.dates {
			rows = append(rows, row(id, 1, date, -2000, fixture.name, "Entertainment"))
			id++
		}
	}

	result := detectAt(t, rows, "2026-07-30")
	byKey := seriesByKey(result.Series)
	for key, cadence := range map[string]string{
		"weekly example":    "weekly",
		"biweekly example":  "biweekly",
		"monthly example":   "monthly",
		"quarterly example": "quarterly",
	} {
		if byKey[key].Cadence != cadence {
			t.Errorf("%s cadence = %q, want %q", key, byKey[key].Cadence, cadence)
		}
	}
	if _, exists := byKey["yearly example"]; exists {
		t.Error("yearly cadence classified, want v1 rejection")
	}
}

func TestDetectRecurringBestFitSkipsExtraAndPrefersRecentOverLongerOldRun(t *testing.T) {
	var rows []Candidate
	for index, date := range []string{
		"2025-06-01", "2025-06-08", "2025-06-15", "2025-06-22",
		"2025-06-29", "2025-07-06", "2025-07-13", "2025-07-20",
		"2026-05-01", "2026-06-01", "2026-07-01",
	} {
		rows = append(rows, row(int64(index+1), 1, date, -1500, "Changing Example", "Entertainment"))
	}
	for index, date := range []string{
		"2026-01-01", "2026-02-01", "2026-02-10", "2026-03-01", "2026-04-01",
	} {
		rows = append(rows, row(int64(index+20), 1, date, -2400, "Extra Charge Example", "Entertainment"))
	}

	result := detectAt(t, rows, "2026-07-30")
	byKey := seriesByKey(result.Series)
	changing := byKey["changing example"]
	if changing.Cadence != "monthly" ||
		!reflect.DeepEqual(changing.MemberTransactionIDs, []int64{9, 10, 11}) {
		t.Errorf("changing series = %+v, want recent monthly ids 9,10,11", changing)
	}
	extra := byKey["extra charge example"]
	if extra.Cadence != "monthly" ||
		!reflect.DeepEqual(extra.MemberTransactionIDs, []int64{20, 21, 23, 24}) {
		t.Errorf("extra series = %+v, want monthly chain skipping id 22", extra)
	}
}

func TestDetectRecurringGraceKeepsNextOnOpenStep(t *testing.T) {
	rows := monthlyRows(1, 1, -1500, "Grace Example", "Entertainment",
		"2026-04-01", "2026-05-01", "2026-06-01")

	due := detectAt(t, rows, "2026-07-01").Series[0]
	if due.NextExpectedDate != "2026-07-01" || due.MissCount != 0 {
		t.Errorf("due-day schedule = %+v, want July 1 with no miss", due)
	}
	inGrace := detectAt(t, rows, "2026-07-02").Series[0]
	if inGrace.NextExpectedDate != "2026-07-01" || inGrace.MissCount != 0 {
		t.Errorf("in-grace schedule = %+v, want July 1 with no miss", inGrace)
	}
}

func TestDetectRecurringInGraceVisibleToBillsHorizonRules(t *testing.T) {
	rows := monthlyRows(1, 1, -1500, "Visible Grace Example", "Entertainment",
		"2026-04-01", "2026-05-01", "2026-06-01")
	asOf := "2026-07-03"
	series := detectAt(t, rows, asOf).Series[0]

	due, err := time.Parse(time.DateOnly, series.NextExpectedDate)
	if err != nil {
		t.Fatalf("parse next expected date: %v", err)
	}
	now, err := time.Parse(time.DateOnly, asOf)
	if err != nil {
		t.Fatalf("parse asOf: %v", err)
	}
	if !series.IsActive || !due.Before(now) || now.After(due.AddDate(0, 0, 3)) {
		t.Errorf("in-grace schedule = %+v, want active past due within bills grace", series)
	}
}

func TestDetectRecurringTwoMissesAfterGraceDeactivates(t *testing.T) {
	rows := monthlyRows(1, 1, -1500, "Missed Example", "Entertainment",
		"2026-01-01", "2026-02-01", "2026-03-01", "2026-04-01")

	series := detectAt(t, rows, "2026-07-02").Series[0]
	if series.IsActive || series.MissCount != 2 || series.NextExpectedDate != "2026-07-01" {
		t.Errorf("missed schedule = %+v, want inactive with two misses and July open", series)
	}
}

func TestDetectRecurringEOMAnchorPreserves31ThroughFebruary(t *testing.T) {
	rows := monthlyRows(1, 1, -1500, "EOM Example", "Entertainment",
		"2026-01-31", "2026-02-28", "2026-03-30", "2026-04-30")

	series := detectAt(t, rows, "2026-05-01").Series[0]
	if series.ScheduleAnchorDay != 31 || series.NextExpectedDate != "2026-05-31" {
		t.Errorf("EOM schedule = %+v, want anchor 31 and May 31", series)
	}
}

func TestDetectRecurringAnchorUsesModeNotLastJitterDay(t *testing.T) {
	rows := monthlyRows(1, 1, -1500, "Mode Example", "Entertainment",
		"2026-01-15", "2026-02-15", "2026-03-15", "2026-04-16")

	series := detectAt(t, rows, "2026-05-01").Series[0]
	if series.ScheduleAnchorDay != 15 || series.NextExpectedDate != "2026-05-15" {
		t.Errorf("mode schedule = %+v, want anchor 15 and May 15", series)
	}
}

func TestDetectRecurringEntityIsolation(t *testing.T) {
	rows := append(
		monthlyRows(1, 1, -1500, "Shared Example", "Entertainment",
			"2026-04-01", "2026-05-01", "2026-06-01"),
		monthlyRows(10, 2, -1500, "Shared Example", "Entertainment",
			"2026-04-01", "2026-05-01", "2026-06-01")...,
	)

	result := detectAt(t, rows, "2026-06-15")
	if len(result.Series) != 2 || result.Series[0].EntityID != 1 || result.Series[1].EntityID != 2 {
		t.Errorf("entity-isolated series = %+v, want entities 1 and 2", result.Series)
	}
}

func TestDetectRecurringOverflowIncrementsSkippedCounter(t *testing.T) {
	rows := monthlyRows(1, 1, math.MinInt64, "Overflow Example", "Entertainment",
		"2026-04-01", "2026-05-01", "2026-06-01")
	rows = append(rows, monthlyRows(10, 1, -1500, "Valid Example", "Entertainment",
		"2026-04-01", "2026-05-01", "2026-06-01")...)

	result := detectAt(t, rows, "2026-06-15")
	if result.SkippedOverflow != 1 || len(result.OverflowIdentities) != 1 {
		t.Fatalf("overflow result = %+v, want one skipped identity", result)
	}
	identity := result.OverflowIdentities[0]
	if identity.EntityID != 1 || identity.DetectKey != "overflow example" || identity.AmountSign != -1 {
		t.Errorf("overflow identity = %+v", identity)
	}
	if len(result.Series) != 1 || result.Series[0].DetectKey != "valid example" {
		t.Errorf("series after overflow = %+v, want valid example", result.Series)
	}
}

func TestRecurringLookbackUsesFirstDayFourteenCalendarMonthsBack(t *testing.T) {
	start, end, err := Lookback("2026-07-30")
	if err != nil {
		t.Fatalf("Lookback() error: %v", err)
	}
	if start != "2025-05-01" || end != "2026-07-30" {
		t.Errorf("Lookback() = %s..%s, want 2025-05-01..2026-07-30", start, end)
	}
}

func TestDetectRecurringUsesDisplayThenNormAndLatestNonEmptyDisplayName(t *testing.T) {
	rows := monthlyRows(1, 1, -1500, "Streambox Example #1", "Entertainment",
		"2026-04-01", "2026-05-01", "2026-06-01")
	rows[1].MerchantDisplay = "Streambox Example #2"
	rows[1].MerchantNorm = "descriptor 2222"
	rows[2].MerchantDisplay = ""
	rows[2].MerchantNorm = "streambox example #3"
	rows[2].MerchantRaw = "TST* STREAMBOX 3333"

	result := detectAt(t, rows, "2026-06-15")
	if len(result.Series) != 1 {
		t.Fatalf("Detect() series = %d, want 1", len(result.Series))
	}
	series := result.Series[0]
	if series.DetectKey != "streambox example" || series.Name != "Streambox Example #2" {
		t.Errorf("display/fallback series = %+v", series)
	}
}

func TestDetectRecurringOneShotMedianClusterDropsOutlier(t *testing.T) {
	rows := []Candidate{
		row(1, 1, "2026-01-01", -5000, "Cluster Example", "Entertainment"),
		row(2, 1, "2026-02-01", -1050, "Cluster Example", "Entertainment"),
		row(3, 1, "2026-03-01", -1000, "Cluster Example", "Entertainment"),
		row(4, 1, "2026-04-01", -1000, "Cluster Example", "Entertainment"),
	}

	series := detectAt(t, rows, "2026-04-15").Series[0]
	if series.ExpectedCents != -1000 ||
		!reflect.DeepEqual(series.MemberTransactionIDs, []int64{2, 3, 4}) {
		t.Errorf("clustered series = %+v, want expected -1000 and ids 2,3,4", series)
	}
}

func TestDetectRecurringDominantCategoryTieBreaksByMagnitude(t *testing.T) {
	rows := []Candidate{
		row(1, 1, "2026-01-01", -1500, "Subscription Tie Example", "Rent and Utilities"),
		row(2, 1, "2026-02-01", -1550, "Subscription Tie Example", "Entertainment"),
		row(3, 1, "2026-03-01", -1500, "Subscription Tie Example", "Rent and Utilities"),
		row(4, 1, "2026-04-01", -1550, "Subscription Tie Example", "Entertainment"),
		row(10, 1, "2026-01-01", -1550, "Bill Tie Example", "Rent and Utilities"),
		row(11, 1, "2026-02-01", -1500, "Bill Tie Example", "Entertainment"),
		row(12, 1, "2026-03-01", -1550, "Bill Tie Example", "Rent and Utilities"),
		row(13, 1, "2026-04-01", -1500, "Bill Tie Example", "Entertainment"),
	}

	byKey := seriesByKey(detectAt(t, rows, "2026-04-15").Series)
	if byKey["subscription tie example"].Kind != "subscription" {
		t.Errorf("subscription tie series = %+v", byKey["subscription tie example"])
	}
	if byKey["bill tie example"].Kind != "bill" {
		t.Errorf("bill tie series = %+v", byKey["bill tie example"])
	}
}

func TestDetectRecurringCategorySumOverflowSkipsIdentity(t *testing.T) {
	amount := -(int64(math.MaxInt64/6) + 1)
	var rows []Candidate
	for index, date := range []string{
		"2026-05-01", "2026-05-08", "2026-05-15",
		"2026-05-22", "2026-05-29", "2026-06-05",
	} {
		rows = append(rows, row(int64(index+1), 1, date, amount,
			"Category Overflow Example", "Rent and Utilities"))
	}

	result := detectAt(t, rows, "2026-06-06")
	if len(result.Series) != 0 || result.SkippedOverflow != 1 ||
		len(result.OverflowIdentities) != 1 {
		t.Errorf("category overflow result = %+v, want one skipped identity", result)
	}
}

func TestDetectKeyV1FrozenFixtures(t *testing.T) {
	fixtures := map[string]string{
		"Netflix":        "netflix",
		"netflix #123":   "netflix",
		"netflix#123":    "netflix",
		"store 1234":     "store",
		"store#99":       "store",
		"store 1234 #99": "store",
		"acme 12":        "acme 12",
	}
	for input, want := range fixtures {
		t.Run(input, func(t *testing.T) {
			if got := DetectKeyV1(input); got != want {
				t.Errorf("DetectKeyV1(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

func row(id, entityID int64, date string, amount int64, merchant, category string) Candidate {
	return Candidate{
		TransactionID:   id,
		EntityID:        entityID,
		Date:            date,
		AmountCents:     amount,
		MerchantDisplay: merchant,
		MerchantNorm:    merchant,
		MerchantRaw:     merchant,
		Category:        category,
		Status:          "posted",
	}
}

func monthlyRows(
	firstID int64,
	entityID int64,
	amount int64,
	merchant string,
	category string,
	dates ...string,
) []Candidate {
	rows := make([]Candidate, 0, len(dates))
	for index, date := range dates {
		rows = append(rows, row(firstID+int64(index), entityID, date, amount, merchant, category))
	}
	return rows
}

func detectAt(t *testing.T, rows []Candidate, asOf string) Result {
	t.Helper()
	result, err := Detect(rows, asOf)
	if err != nil {
		t.Fatalf("Detect(%s) error: %v", asOf, err)
	}
	return result
}

func seriesByKey(series []Series) map[string]Series {
	byKey := make(map[string]Series, len(series))
	for _, item := range series {
		byKey[item.DetectKey] = item
	}
	return byKey
}

func TestDetectResultOrderingIsDeterministic(t *testing.T) {
	rows := append(
		monthlyRows(1, 2, -1500, "Zulu Example", "Entertainment",
			"2026-04-01", "2026-05-01", "2026-06-01"),
		monthlyRows(10, 1, 1500, "Alpha Example", "Income",
			"2026-04-01", "2026-05-01", "2026-06-01")...,
	)
	result := detectAt(t, rows, "2026-06-15")
	got := make([]string, 0, len(result.Series))
	for _, series := range result.Series {
		got = append(got, series.DetectKey)
	}
	want := append([]string(nil), got...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("series order = %v, want deterministic entity/key order %v", got, want)
	}
}
