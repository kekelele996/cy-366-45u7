package service

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/esportsbar/backend/internal/constants"
	"github.com/esportsbar/backend/internal/dto"
	"github.com/esportsbar/backend/internal/model"
	"github.com/esportsbar/backend/internal/repository"
	"github.com/esportsbar/backend/internal/util"
)

// RescheduleMinLeadMinutes 改期最小提前量（分钟）：开始前 30 分钟以上才允许改期。
const RescheduleMinLeadMinutes = 30

// ReservationService 机位预约服务。
type ReservationService struct {
	reservationRepo *repository.ReservationRepository
	stationService  *StationService
	db              *gorm.DB
	logger          *slog.Logger
}

// NewReservationService 构造预约服务。
func NewReservationService(
	reservationRepo *repository.ReservationRepository,
	stationService *StationService,
	db *gorm.DB,
	logger *slog.Logger,
) *ReservationService {
	return &ReservationService{reservationRepo: reservationRepo, stationService: stationService, db: db, logger: logger}
}

// Create 创建预约：校验时段冲突，事务内锁定机位并落库。
func (s *ReservationService) Create(userID uint, req *dto.CreateReservationReq) (*model.Reservation, error) {
	if !req.EndTime.After(req.StartTime) {
		return nil, util.NewAppError(constants.CodeValidation, "预约结束时间必须晚于开始时间")
	}
	station, err := s.stationService.GetByID(req.StationID)
	if err != nil {
		return nil, err
	}
	if station.Status != constants.StationIdle && station.Status != constants.StationReserved {
		return nil, util.NewAppError(constants.CodeStationBusy, "机位当前不可预约，请选择其他机位")
	}
	cnt, err := s.reservationRepo.CountConflict(req.StationID, req.StartTime, req.EndTime, 0)
	if err != nil {
		return nil, fmt.Errorf("reservation count conflict: %w", err)
	}
	if cnt > 0 {
		return nil, util.NewAppError(constants.CodeReservation, "该机位时段已被预约，请更换时段")
	}
	res := &model.Reservation{
		UserID:    userID,
		StationID: req.StationID,
		StartTime: req.StartTime,
		EndTime:   req.EndTime,
		Status:    constants.ReservationConfirmed,
		Remark:    req.Remark,
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		locked, err := s.stationService.LockForUpdate(tx, req.StationID)
		if err != nil {
			return err
		}
		if locked.Status == constants.StationIdle {
			locked.Status = constants.StationReserved
			if err := tx.Save(locked).Error; err != nil {
				return err
			}
		}
		return s.reservationRepo.Create(res)
	})
	if err != nil {
		return nil, fmt.Errorf("reservation create tx: %w", err)
	}
	s.logger.Info(fmt.Sprintf(constants.LogTemplates["reservation_create_ok"], userID, req.StationID, req.StartTime.Format("2006-01-02 15:04")))
	return res, nil
}

// Confirm 确认预约（staff/admin）。
func (s *ReservationService) Confirm(id uint) (*model.Reservation, error) {
	res, err := s.getReservation(id)
	if err != nil {
		return nil, err
	}
	if res.Status != constants.ReservationPending && res.Status != constants.ReservationConfirmed {
		return nil, util.NewAppError(constants.CodeReservation, "仅待确认或已确认的预约可以确认")
	}
	res.Status = constants.ReservationConfirmed
	if err := s.reservationRepo.Update(res); err != nil {
		return nil, fmt.Errorf("reservation confirm: %w", err)
	}
	s.logger.Info(fmt.Sprintf(constants.LogTemplates["reservation_confirm_ok"], id))
	return res, nil
}

// Cancel 取消预约：释放机位预约状态。
func (s *ReservationService) Cancel(id uint, userID uint) (*model.Reservation, error) {
	res, err := s.getReservation(id)
	if err != nil {
		return nil, err
	}
	if res.Status != constants.ReservationPending && res.Status != constants.ReservationConfirmed && res.Status != constants.ReservationCheckedIn {
		return nil, util.NewAppError(constants.CodeReservation, "当前状态不可取消")
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		res.Status = constants.ReservationCancelled
		if err := s.reservationRepo.Update(res); err != nil {
			return err
		}
		return releaseStationReserved(tx, s.stationService, res.StationID)
	})
	if err != nil {
		return nil, fmt.Errorf("reservation cancel tx: %w", err)
	}
	s.logger.Info(fmt.Sprintf(constants.LogTemplates["reservation_cancel_ok"], id))
	return res, nil
}

// Reschedule 预约改期：开始前 30 分钟以上且待确认/已确认时，可换机位、换时段。
// 新时段与目标机位已有有效预约重叠则返回冲突错误，原预约保留不变；
// 改到其他机位且原机位无其他有效预约时，原机位恢复空闲，目标机位进入已预约。
func (s *ReservationService) Reschedule(id uint, userID uint, role string, req *dto.RescheduleReservationReq) (*model.Reservation, error) {
	if !req.EndTime.After(req.StartTime) {
		return nil, util.NewAppError(constants.CodeValidation, "预约改期结束时间必须晚于开始时间")
	}
	res, err := s.getReservation(id)
	if err != nil {
		return nil, err
	}
	if role == constants.RoleMember && res.UserID != userID {
		return nil, util.NewAppError(constants.CodeForbidden, "会员无权改期他人预约，请使用本人账号操作")
	}
	if err := canReschedule(res.Status, res.StartTime, time.Now()); err != nil {
		return nil, err
	}
	station, err := s.stationService.GetByID(req.StationID)
	if err != nil {
		return nil, err
	}
	if station.Status != constants.StationIdle && station.Status != constants.StationReserved {
		return nil, util.NewAppError(constants.CodeStationBusy, "目标机位当前不可预约，请选择其他机位")
	}
	oldStationID := res.StationID
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// 换机位时按机位 ID 升序加锁，避免并发改期互相死锁。
		firstID, secondID := oldStationID, req.StationID
		if secondID < firstID {
			firstID, secondID = secondID, firstID
		}
		stations := map[uint]*model.Station{}
		locked, err := s.stationService.LockForUpdate(tx, firstID)
		if err != nil {
			return err
		}
		stations[firstID] = locked
		if secondID != firstID {
			locked, err = s.stationService.LockForUpdate(tx, secondID)
			if err != nil {
				return err
			}
			stations[secondID] = locked
		}
		target := stations[req.StationID]
		if target.Status != constants.StationIdle && target.Status != constants.StationReserved {
			return util.NewAppError(constants.CodeStationBusy, "目标机位当前不可预约，请选择其他机位")
		}
		// 锁机位后复查时段冲突：冲突则回滚，原预约继续保留。
		cnt, err := s.reservationRepo.CountConflictTx(tx, req.StationID, req.StartTime, req.EndTime, res.ID)
		if err != nil {
			return err
		}
		if cnt > 0 {
			return util.NewAppError(constants.CodeConflict, "目标机位在该时段已有有效预约，改期失败，原预约继续保留")
		}
		res.StationID = req.StationID
		res.StartTime = req.StartTime
		res.EndTime = req.EndTime
		if err := s.reservationRepo.UpdateTx(tx, res); err != nil {
			return err
		}
		if req.StationID != oldStationID {
			// 原机位没有其他有效预约时恢复空闲。
			remain, err := s.reservationRepo.CountActiveByStationTx(tx, oldStationID, res.ID)
			if err != nil {
				return err
			}
			oldStation := stations[oldStationID]
			if remain == 0 && oldStation.Status == constants.StationReserved {
				oldStation.Status = constants.StationIdle
				if err := tx.Save(oldStation).Error; err != nil {
					return err
				}
			}
			// 目标机位进入已预约。
			if target.Status == constants.StationIdle {
				target.Status = constants.StationReserved
				if err := tx.Save(target).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reservation reschedule tx: %w", err)
	}
	s.logger.Info(fmt.Sprintf(constants.LogTemplates["reservation_reschedule_ok"], id, oldStationID, req.StationID, req.StartTime.Format("2006-01-02 15:04")))
	return res, nil
}

// canReschedule 改期状态机：仅待确认/已确认且距开始时间 30 分钟以上可改期。
func canReschedule(status string, startTime, now time.Time) error {
	if status != constants.ReservationPending && status != constants.ReservationConfirmed {
		return util.NewAppError(constants.CodeReservation,
			fmt.Sprintf("预约状态为 %s，仅待确认或已确认的预约可以改期", util.StatusText(status)))
	}
	if startTime.Sub(now) < RescheduleMinLeadMinutes*time.Minute {
		return util.NewAppError(constants.CodeReservation,
			fmt.Sprintf("距开始时间不足 %d 分钟，预约不可改期", RescheduleMinLeadMinutes))
	}
	return nil
}

// CheckIn 到店扫码开机：预约状态流转为 checked_in，机位置为使用中。
func (s *ReservationService) CheckIn(id uint) (*model.Reservation, error) {
	res, err := s.getReservation(id)
	if err != nil {
		return nil, err
	}
	if res.Status != constants.ReservationConfirmed {
		return nil, util.NewAppError(constants.CodeReservation, "仅已确认的预约可以开机")
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		res.Status = constants.ReservationCheckedIn
		if err := s.reservationRepo.Update(res); err != nil {
			return err
		}
		station, err := s.stationService.LockForUpdate(tx, res.StationID)
		if err != nil {
			return err
		}
		station.Status = constants.StationUsing
		return tx.Save(station).Error
	})
	if err != nil {
		return nil, fmt.Errorf("reservation checkin tx: %w", err)
	}
	s.logger.Info(fmt.Sprintf(constants.LogTemplates["reservation_checkin_ok"], id))
	return res, nil
}

// GetByID 查询预约详情。
func (s *ReservationService) GetByID(id uint) (*model.Reservation, error) {
	return s.getReservation(id)
}

// List 分页查询预约。
func (s *ReservationService) List(query *dto.ReservationQuery) ([]model.Reservation, int64, error) {
	page := query.Page
	pageSize := query.PageSize
	if page <= 0 {
		page = constants.DefaultPage
	}
	if pageSize <= 0 {
		pageSize = constants.DefaultPageSize
	}
	return s.reservationRepo.List(page, pageSize, query.Status, query.UserID)
}

// getReservation 查询预约并统一处理错误。
func (s *ReservationService) getReservation(id uint) (*model.Reservation, error) {
	res, err := s.reservationRepo.FindByID(id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, util.NewAppError(constants.CodeNotFound, "预约记录不存在")
		}
		return nil, fmt.Errorf("reservation find: %w", err)
	}
	return res, nil
}

// releaseStationReserved 将机位从预约状态释放为空闲。
func releaseStationReserved(tx *gorm.DB, svc *StationService, stationID uint) error {
	station, err := svc.LockForUpdate(tx, stationID)
	if err != nil {
		return err
	}
	if station.Status == constants.StationReserved {
		station.Status = constants.StationIdle
		return tx.Save(station).Error
	}
	return nil
}
