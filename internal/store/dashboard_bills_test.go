package store

import (
	"context"
	"reflect"
	"testing"
)

func TestDashboardBillsNullWhenNeverRunOrError(t *testing.T) {
	for _, status := range []string{"never_run", "error"} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			db := openTestDB(t)
			entityID := insertEntity(t, db, "personal", "Personal")
			if status == "error" {
				setBillsDetector(t, db, DetectorState{Status: "error"})
			}
			insertBillsRecurring(t, db, entityID, "Hidden Detected Bill", "bill", "monthly", -1200, "2026-07-15", true, 15)
			cardID := insertBillsAccount(t, db, entityID, "Visible CLI Card", "credit_card", true)
			insertBillsCardTerms(t, db, cardID, int64Pointer(2500), int64Pointer(20), stringPointer("2026-07-20"))

			dashboard, err := ReadDashboard(ctx, db, DashboardFilter{
				From: "2026-07-01", To: "2026-07-31", BillsAsOf: "2026-07-01",
			})
			if err != nil {
				t.Fatalf("ReadDashboard() error: %v", err)
			}
			if dashboard.UpcomingBills != nil {
				t.Errorf("status %s upcoming bills = %+v, want nil", status, dashboard.UpcomingBills)
			}
			if dashboard.RecurringDetect.Status != status {
				t.Errorf("detector status = %q, want %q", dashboard.RecurringDetect.Status, status)
			}
		})
	}
}

func TestDashboardBillsEmptyWhenOkNothingDue(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	setBillsDetector(t, db, DetectorState{Status: "ok"})
	dashboard, err := ReadDashboard(ctx, db, DashboardFilter{
		From: "2026-07-01", To: "2026-07-31", BillsAsOf: "2026-07-01",
	})
	if err != nil {
		t.Fatalf("ReadDashboard() error: %v", err)
	}
	if dashboard.UpcomingBills == nil {
		t.Fatal("ok upcoming bills = nil, want empty report")
	}
	if len(dashboard.UpcomingBills.Items) != 0 {
		t.Errorf("upcoming bills = %+v, want empty", dashboard.UpcomingBills.Items)
	}
}

func TestDashboardFillsUpcomingBillsWhenOk(t *testing.T) {
	for _, status := range []string{"ok", "partial"} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			db := openTestDB(t)
			entityID := insertEntity(t, db, "personal", "Personal")
			setBillsDetector(t, db, DetectorState{Status: status})
			insertBillsRecurring(t, db, entityID, "Dashboard Subscription", "subscription", "monthly", -1800, "2026-07-10", true, 10)
			cardID := insertBillsAccount(t, db, entityID, "Dashboard Card", "credit_card", true)
			insertBillsCardTerms(t, db, cardID, int64Pointer(3200), int64Pointer(20), stringPointer("2026-07-20"))

			want, err := ReadBills(ctx, db, "2026-07-01", 30)
			if err != nil {
				t.Fatalf("ReadBills() error: %v", err)
			}
			dashboard, err := ReadDashboard(ctx, db, DashboardFilter{
				From: "2026-07-01", To: "2026-07-31", BillsAsOf: "2026-07-01",
			})
			if err != nil {
				t.Fatalf("ReadDashboard() error: %v", err)
			}
			if dashboard.UpcomingBills == nil {
				t.Fatal("upcoming bills = nil, want populated report")
			}
			if !reflect.DeepEqual(*dashboard.UpcomingBills, want) {
				t.Errorf("dashboard bills = %+v, want shared ReadBills %+v", *dashboard.UpcomingBills, want)
			}
		})
	}
}
