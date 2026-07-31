package store

import (
	"context"
	"math"
	"testing"
)

func TestDashboardAnomaliesIncludePeriodAndSkippedOverflow(t *testing.T) {
	t.Run("anomaly payload with detector error keeps bills null", func(t *testing.T) {
		ctx := context.Background()
		db := openTestDB(t)
		entityID := insertEntity(t, db, "personal", "Personal")
		setBillsDetector(t, db, DetectorState{Status: "error"})
		accountID := insertAnomalyAccount(t, db, entityID, "Dashboard Checking", "dashboard-anomaly")
		spikeID := insertAnomalyCategory(t, db, "Dashboard Spike", nil)
		overflowID := insertAnomalyCategory(t, db, "Dashboard Boundary", nil)
		baselineSpend := int64(math.MaxInt64/2 + 100)
		sequence := 0
		for _, date := range []string{"2026-01-15", "2026-02-15", "2026-03-15"} {
			sequence++
			insertAnomalyTransaction(t, db, accountID, entityID, date, -10000,
				&spikeID, "posted", false, false, sequence)
			sequence++
			insertAnomalyTransaction(t, db, accountID, entityID, date, -baselineSpend,
				&overflowID, "posted", false, false, sequence)
		}
		sequence++
		insertAnomalyTransaction(t, db, accountID, entityID, "2026-04-15", -31000,
			&spikeID, "posted", false, false, sequence)
		sequence++
		insertAnomalyTransaction(t, db, accountID, entityID, "2026-04-15", -10000,
			&overflowID, "posted", false, false, sequence)

		dashboard, err := ReadDashboard(ctx, db, DashboardFilter{
			From: "2026-05-01", To: "2026-05-31", BillsAsOf: "2026-05-20",
		})
		if err != nil {
			t.Fatalf("ReadDashboard() error: %v", err)
		}
		if dashboard.Anomalies.Period != "2026-04" ||
			dashboard.Anomalies.SkippedOverflow != 1 || len(dashboard.Anomalies.Items) != 1 {
			t.Errorf("dashboard anomalies = %+v, want April, one spike, one skipped", dashboard.Anomalies)
		}
		if item := dashboard.Anomalies.Items[0]; item.Category != "Dashboard Spike" ||
			item.SpendCents != 31000 || item.BaselineCents != 10000 || item.DeltaCents != 21000 {
			t.Errorf("dashboard anomaly item = %+v", item)
		}
		if dashboard.UpcomingBills != nil {
			t.Errorf("error detector upcoming bills = %+v, want nil", dashboard.UpcomingBills)
		}
		if dashboard.RecurringDetect.Status != "error" {
			t.Errorf("recurring detector = %+v, want error", dashboard.RecurringDetect)
		}
	})

	t.Run("empty anomalies remain real and partial bills remain empty", func(t *testing.T) {
		ctx := context.Background()
		db := openTestDB(t)
		setBillsDetector(t, db, DetectorState{Status: "partial"})
		dashboard, err := ReadDashboard(ctx, db, DashboardFilter{
			From: "2026-05-01", To: "2026-05-31", BillsAsOf: "2026-05-20",
		})
		if err != nil {
			t.Fatalf("ReadDashboard() error: %v", err)
		}
		if dashboard.Anomalies.Period != "2026-04" ||
			dashboard.Anomalies.SkippedOverflow != 0 || len(dashboard.Anomalies.Items) != 0 {
			t.Errorf("empty dashboard anomalies = %+v", dashboard.Anomalies)
		}
		if dashboard.UpcomingBills == nil || len(dashboard.UpcomingBills.Items) != 0 {
			t.Errorf("partial detector upcoming bills = %+v, want non-nil empty", dashboard.UpcomingBills)
		}
		if dashboard.RecurringDetect.Status != "partial" {
			t.Errorf("recurring detector = %+v, want partial", dashboard.RecurringDetect)
		}
	})
}
