package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/esportsbar/backend/internal/model"
)

// ReservationRepository 预约仓储。
type ReservationRepository struct {
	db *gorm.DB
}

// NewReservationRepository 构造预约仓储。
func NewReservationRepository(db *gorm.DB) *ReservationRepository {
	return &ReservationRepository{db: db}
}

// Create 创建预约。
func (r *ReservationRepository) Create(res *model.Reservation) error {
	return r.db.Create(res).Error
}

// FindByID 查询预约。
func (r *ReservationRepository) FindByID(id uint) (*model.Reservation, error) {
	var res model.Reservation
	err := r.db.First(&res, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &res, err
}

// Update 更新预约。
func (r *ReservationRepository) Update(res *model.Reservation) error {
	return r.db.Save(res).Error
}

// LockByID 事务内行锁查询预约（并发改期等状态流转使用）。
func (r *ReservationRepository) LockByID(tx *gorm.DB, id uint) (*model.Reservation, error) {
	var res model.Reservation
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&res, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &res, err
}

// List 分页查询预约。
func (r *ReservationRepository) List(page, pageSize int, status string, userID uint) ([]model.Reservation, int64, error) {
	var list []model.Reservation
	var total int64
	query := r.db.Model(&model.Reservation{})
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if userID > 0 {
		query = query.Where("user_id = ?", userID)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := query.Order("start_time DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// CountConflict 统计机位在时段内的冲突预约数。
func (r *ReservationRepository) CountConflict(stationID uint, start, end time.Time, excludeID uint) (int64, error) {
	return CountConflict(r.db, stationID, start, end, excludeID)
}

// CountConflictTx 统计冲突预约数，使用调用方传入的句柄（事务内复查时传 tx）。
func (r *ReservationRepository) CountConflictTx(tx *gorm.DB, stationID uint, start, end time.Time, excludeID uint) (int64, error) {
	return CountConflict(tx, stationID, start, end, excludeID)
}

// CountConflict 共享冲突统计：有效预约（pending/confirmed/checked_in）且时段重叠。
func CountConflict(db *gorm.DB, stationID uint, start, end time.Time, excludeID uint) (int64, error) {
	var cnt int64
	query := db.Model(&model.Reservation{}).
		Where("station_id = ? AND status IN ?", stationID, []string{"pending", "confirmed", "checked_in"}).
		Where("start_time < ? AND end_time > ?", end, start)
	if excludeID > 0 {
		query = query.Where("id <> ?", excludeID)
	}
	err := query.Count(&cnt).Error
	return cnt, err
}

// CountActive 统计机位当前的有效预约数（pending/confirmed/checked_in）。
func (r *ReservationRepository) CountActive(stationID uint, excludeID uint) (int64, error) {
	return CountActive(r.db, stationID, excludeID)
}

// CountActiveTx 统计有效预约数，使用调用方传入的句柄（事务内传 tx）。
func (r *ReservationRepository) CountActiveTx(tx *gorm.DB, stationID uint, excludeID uint) (int64, error) {
	return CountActive(tx, stationID, excludeID)
}

// CountActive 共享统计：机位上的有效预约数量。
func CountActive(db *gorm.DB, stationID uint, excludeID uint) (int64, error) {
	var cnt int64
	query := db.Model(&model.Reservation{}).
		Where("station_id = ? AND status IN ?", stationID, []string{"pending", "confirmed", "checked_in"})
	if excludeID > 0 {
		query = query.Where("id <> ?", excludeID)
	}
	err := query.Count(&cnt).Error
	return cnt, err
}
