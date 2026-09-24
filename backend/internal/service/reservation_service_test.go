package service

import (
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"github.com/esportsbar/backend/internal/constants"
	"github.com/esportsbar/backend/internal/dto"
	"github.com/esportsbar/backend/internal/repository"
	"github.com/esportsbar/backend/internal/util"
)

// TestCanReschedule 改期状态机白盒测试：状态与 30 分钟提前量门槛。
func TestCanReschedule(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.Local)
	cases := []struct {
		name      string
		status    string
		startTime time.Time
		wantCode  int // 0 表示允许改期
	}{
		{name: "pending_far_enough", status: constants.ReservationPending, startTime: now.Add(2 * time.Hour), wantCode: 0},
		{name: "confirmed_far_enough", status: constants.ReservationConfirmed, startTime: now.Add(31 * time.Minute), wantCode: 0},
		{name: "confirmed_exactly_30min", status: constants.ReservationConfirmed, startTime: now.Add(RescheduleMinLeadMinutes * time.Minute), wantCode: 0},
		{name: "confirmed_too_close", status: constants.ReservationConfirmed, startTime: now.Add(29 * time.Minute), wantCode: constants.CodeReservation},
		{name: "pending_already_started", status: constants.ReservationPending, startTime: now.Add(-time.Minute), wantCode: constants.CodeReservation},
		{name: "checked_in_rejected", status: constants.ReservationCheckedIn, startTime: now.Add(2 * time.Hour), wantCode: constants.CodeReservation},
		{name: "completed_rejected", status: constants.ReservationCompleted, startTime: now.Add(2 * time.Hour), wantCode: constants.CodeReservation},
		{name: "cancelled_rejected", status: constants.ReservationCancelled, startTime: now.Add(2 * time.Hour), wantCode: constants.CodeReservation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := canReschedule(tc.status, tc.startTime, now)
			if tc.wantCode == 0 {
				if err != nil {
					t.Fatalf("canReschedule(%s) unexpected err: %v", tc.name, err)
				}
				return
			}
			var appErr *util.AppError
			if !errors.As(err, &appErr) {
				t.Fatalf("canReschedule(%s) err type = %T, want *util.AppError", tc.name, err)
			}
			if appErr.Code != tc.wantCode {
				t.Fatalf("canReschedule(%s) code = %d, want %d", tc.name, appErr.Code, tc.wantCode)
			}
		})
	}
}

// newReservationMockDB 构造 sqlmock 驱动的 GORM 连接，用于改期事务编排测试。
func newReservationMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock new error: %v", err)
	}
	gdb, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      db,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm open error: %v", err)
	}
	return gdb, mock
}

func stationRow(id uint, status string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "name", "area", "station_type", "price_per_hour", "status", "description", "created_at", "updated_at"}).
		AddRow(id, "A区-01", "A区", "seat", 10, status, "", time.Now(), time.Now())
}

func reservationRow(id, userID, stationID uint, status string, start, end time.Time) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "user_id", "station_id", "start_time", "end_time", "status", "remark", "created_at", "updated_at"}).
		AddRow(id, userID, stationID, start, end, status, "", time.Now(), time.Now())
}

var (
	reservationQueryRe = regexp.MustCompile("SELECT \\* FROM `reservations`")
	stationQueryRe     = regexp.MustCompile("SELECT \\* FROM `stations`")
	stationLockRe      = regexp.MustCompile("SELECT \\* FROM `stations`.*FOR UPDATE")
	reservationCountRe = regexp.MustCompile("SELECT count\\(\\*\\) FROM `reservations`")
	reservationSaveRe  = regexp.MustCompile("UPDATE `reservations`")
	stationSaveRe      = regexp.MustCompile("UPDATE `stations`")
)

// TestRescheduleSwitchStation 改到其他机位：原机位无其他有效预约时恢复空闲，目标机位进入已预约。
func TestRescheduleSwitchStation(t *testing.T) {
	gdb, mock := newReservationMockDB(t)
	svc := NewReservationService(repository.NewReservationRepository(gdb), NewStationService(repository.NewStationRepository(gdb), newTestLogger()), gdb, newTestLogger())
	now := time.Now()
	start := now.Add(2 * time.Hour)
	end := now.Add(4 * time.Hour)

	mock.ExpectQuery(reservationQueryRe.String()).WillReturnRows(reservationRow(1, 7, 1, constants.ReservationConfirmed, start, end))
	mock.ExpectQuery(stationQueryRe.String()).WillReturnRows(stationRow(2, "idle"))
	mock.ExpectBegin()
	mock.ExpectQuery(stationLockRe.String()).WillReturnRows(stationRow(1, "reserved")) // 原机位
	mock.ExpectQuery(stationLockRe.String()).WillReturnRows(stationRow(2, "idle"))     // 目标机位
	mock.ExpectQuery(reservationCountRe.String()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(reservationSaveRe.String()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(reservationCountRe.String()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(stationSaveRe.String()).WillReturnResult(sqlmock.NewResult(0, 1)) // 原机位 -> idle
	mock.ExpectExec(stationSaveRe.String()).WillReturnResult(sqlmock.NewResult(0, 1)) // 目标机位 -> reserved
	mock.ExpectCommit()

	res, err := svc.Reschedule(1, 7, constants.RoleMember, &dto.RescheduleReservationReq{StationID: 2, StartTime: start, EndTime: end})
	if err != nil {
		t.Fatalf("Reschedule error: %v", err)
	}
	if res.StationID != 2 || !res.StartTime.Equal(start) || !res.EndTime.Equal(end) {
		t.Fatalf("unexpected rescheduled reservation: %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}

// TestRescheduleConflictKeepsOriginal 新时段与目标机位已有有效预约重叠：提示冲突并回滚，原预约保留。
func TestRescheduleConflictKeepsOriginal(t *testing.T) {
	gdb, mock := newReservationMockDB(t)
	svc := NewReservationService(repository.NewReservationRepository(gdb), NewStationService(repository.NewStationRepository(gdb), newTestLogger()), gdb, newTestLogger())
	now := time.Now()
	start := now.Add(2 * time.Hour)
	end := now.Add(4 * time.Hour)

	mock.ExpectQuery(reservationQueryRe.String()).WillReturnRows(reservationRow(1, 7, 1, constants.ReservationConfirmed, start, end))
	mock.ExpectQuery(stationQueryRe.String()).WillReturnRows(stationRow(2, "reserved"))
	mock.ExpectBegin()
	mock.ExpectQuery(stationLockRe.String()).WillReturnRows(stationRow(1, "reserved"))
	mock.ExpectQuery(stationLockRe.String()).WillReturnRows(stationRow(2, "reserved"))
	mock.ExpectQuery(reservationCountRe.String()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectRollback()

	_, err := svc.Reschedule(1, 7, constants.RoleMember, &dto.RescheduleReservationReq{StationID: 2, StartTime: start, EndTime: end})
	var appErr *util.AppError
	if !errors.As(err, &appErr) || appErr.Code != constants.CodeConflict {
		t.Fatalf("expected CodeConflict, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}

// TestRescheduleSameStation 只换时段不换机位：不触碰机位状态。
func TestRescheduleSameStation(t *testing.T) {
	gdb, mock := newReservationMockDB(t)
	svc := NewReservationService(repository.NewReservationRepository(gdb), NewStationService(repository.NewStationRepository(gdb), newTestLogger()), gdb, newTestLogger())
	now := time.Now()
	start := now.Add(3 * time.Hour)
	end := now.Add(5 * time.Hour)

	mock.ExpectQuery(reservationQueryRe.String()).WillReturnRows(reservationRow(1, 7, 1, constants.ReservationPending, now.Add(time.Hour), now.Add(2*time.Hour)))
	mock.ExpectQuery(stationQueryRe.String()).WillReturnRows(stationRow(1, "reserved"))
	mock.ExpectBegin()
	mock.ExpectQuery(stationLockRe.String()).WillReturnRows(stationRow(1, "reserved"))
	mock.ExpectQuery(reservationCountRe.String()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(reservationSaveRe.String()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res, err := svc.Reschedule(1, 7, constants.RoleMember, &dto.RescheduleReservationReq{StationID: 1, StartTime: start, EndTime: end})
	if err != nil {
		t.Fatalf("Reschedule error: %v", err)
	}
	if res.StationID != 1 || !res.StartTime.Equal(start) {
		t.Fatalf("unexpected rescheduled reservation: %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}

// TestRescheduleForbiddenForOtherMember 会员不能改期他人预约。
func TestRescheduleForbiddenForOtherMember(t *testing.T) {
	gdb, mock := newReservationMockDB(t)
	svc := NewReservationService(repository.NewReservationRepository(gdb), NewStationService(repository.NewStationRepository(gdb), newTestLogger()), gdb, newTestLogger())
	now := time.Now()

	mock.ExpectQuery(reservationQueryRe.String()).WillReturnRows(reservationRow(1, 7, 1, constants.ReservationConfirmed, now.Add(2*time.Hour), now.Add(4*time.Hour)))

	_, err := svc.Reschedule(1, 8, constants.RoleMember, &dto.RescheduleReservationReq{StationID: 1, StartTime: now.Add(3 * time.Hour), EndTime: now.Add(5 * time.Hour)})
	var appErr *util.AppError
	if !errors.As(err, &appErr) || appErr.Code != constants.CodeForbidden {
		t.Fatalf("expected CodeForbidden, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}

// TestRescheduleTooCloseToStart 开始前 30 分钟内不可改期。
func TestRescheduleTooCloseToStart(t *testing.T) {
	gdb, mock := newReservationMockDB(t)
	svc := NewReservationService(repository.NewReservationRepository(gdb), NewStationService(repository.NewStationRepository(gdb), newTestLogger()), gdb, newTestLogger())
	now := time.Now()

	mock.ExpectQuery(reservationQueryRe.String()).WillReturnRows(reservationRow(1, 7, 1, constants.ReservationConfirmed, now.Add(10*time.Minute), now.Add(2*time.Hour)))

	_, err := svc.Reschedule(1, 7, constants.RoleMember, &dto.RescheduleReservationReq{StationID: 1, StartTime: now.Add(3 * time.Hour), EndTime: now.Add(5 * time.Hour)})
	var appErr *util.AppError
	if !errors.As(err, &appErr) || appErr.Code != constants.CodeReservation {
		t.Fatalf("expected CodeReservation, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}
