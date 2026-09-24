package service

import (
	"testing"
	"time"

	"github.com/esportsbar/backend/internal/constants"
	"github.com/esportsbar/backend/internal/model"
)

// TestCanReschedule 预约改期准入规则白盒测试：
// 仅待确认/已确认状态、且距开始时间不少于 30 分钟时允许改期。
func TestCanReschedule(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.Local)
	cases := []struct {
		name      string
		status    string
		startTime time.Time
		want      bool
	}{
		{name: "confirmed_over_30min", status: constants.ReservationConfirmed, startTime: now.Add(time.Hour), want: true},
		{name: "pending_over_30min", status: constants.ReservationPending, startTime: now.Add(31 * time.Minute), want: true},
		{name: "exactly_30min", status: constants.ReservationConfirmed, startTime: now.Add(30 * time.Minute), want: true},
		{name: "less_than_30min", status: constants.ReservationConfirmed, startTime: now.Add(29 * time.Minute), want: false},
		{name: "already_started", status: constants.ReservationConfirmed, startTime: now.Add(-time.Minute), want: false},
		{name: "checked_in", status: constants.ReservationCheckedIn, startTime: now.Add(time.Hour), want: false},
		{name: "cancelled", status: constants.ReservationCancelled, startTime: now.Add(time.Hour), want: false},
		{name: "completed", status: constants.ReservationCompleted, startTime: now.Add(time.Hour), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := &model.Reservation{ID: 1, Status: tc.status, StartTime: tc.startTime}
			got, _ := canReschedule(res, now)
			if got != tc.want {
				t.Fatalf("canReschedule(status=%s, start=%s) = %v, want %v", tc.status, tc.startTime.Format(time.RFC3339), got, tc.want)
			}
		})
	}
}
