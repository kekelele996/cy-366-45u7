package service

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/esportsbar/backend/internal/constants"
	"github.com/esportsbar/backend/internal/dto"
	"github.com/esportsbar/backend/internal/model"
	"github.com/esportsbar/backend/internal/repository"
	"github.com/esportsbar/backend/internal/util"
)

// rescheduleLeadTime 改期截止时限：开始前 30 分钟。
const rescheduleLeadTime = 30 * time.Minute

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

// Reschedule 预约改期：开始前 30 分钟以上且待确认/已确认时可换机位、换时段。
// 目标机位与新时段存在有效预约冲突时返回错误，原预约保持不变；
// 换到其他机位且原机位没有其他有效预约时，原机位恢复空闲，目标机位进入已预约。
func (s *ReservationService) Reschedule(id uint, req *dto.RescheduleReservationReq) (*model.Reservation, error) {
	if !req.EndTime.After(req.StartTime) {
		return nil, util.NewAppError(constants.CodeValidation, "预约结束时间必须晚于开始时间")
	}
	res, err := s.getReservation(id)
	if err != nil {
		return nil, err
	}
	if ok, reason := canReschedule(res, time.Now()); !ok {
		return nil, util.NewAppError(constants.CodeReservation, reason)
	}
	target, err := s.stationService.GetByID(req.StationID)
	if err != nil {
		return nil, err
	}
	if target.Status != constants.StationIdle && target.Status != constants.StationReserved {
		return nil, util.NewAppError(constants.CodeStationBusy, "目标机位当前不可预约，请选择其他机位")
	}
	// 事务外预检冲突；冲突时原预约继续保留。
	cnt, err := s.reservationRepo.CountConflict(req.StationID, req.StartTime, req.EndTime, id)
	if err != nil {
		return nil, fmt.Errorf("reservation reschedule count conflict: %w", err)
	}
	if cnt > 0 {
		return nil, util.NewAppError(constants.CodeReservation, "新时段与目标机位的已有预约冲突，请更换机位或时段")
	}

	oldStationID := res.StationID
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// 先锁定预约行，串行化同一条预约的并发改期；再按机位 ID 升序加锁，避免交叉加锁死锁。
		lockedRes, err := s.reservationRepo.LockByID(tx, id)
		if err != nil {
			return err
		}
		if ok, reason := canReschedule(lockedRes, time.Now()); !ok {
			return util.NewAppError(constants.CodeReservation, reason)
		}
		lockedStations, err := s.lockStationsOrdered(tx, oldStationID, req.StationID)
		if err != nil {
			return err
		}
		targetLocked := lockedStations[req.StationID]
		if targetLocked.Status != constants.StationIdle && targetLocked.Status != constants.StationReserved {
			return util.NewAppError(constants.CodeStationBusy, "目标机位当前不可预约，请选择其他机位")
		}
		// 加锁后复查目标机位时段冲突。
		lockedCnt, err := s.reservationRepo.CountConflictTx(tx, req.StationID, req.StartTime, req.EndTime, id)
		if err != nil {
			return err
		}
		if lockedCnt > 0 {
			return util.NewAppError(constants.CodeReservation, "新时段与目标机位的已有预约冲突，请更换机位或时段")
		}
		// 更新预约的机位与时段，状态（待确认/已确认）保持不变。
		res.StationID = req.StationID
		res.StartTime = req.StartTime
		res.EndTime = req.EndTime
		if err := tx.Save(res).Error; err != nil {
			return err
		}
		// 目标机位进入已预约。
		if targetLocked.Status == constants.StationIdle {
			targetLocked.Status = constants.StationReserved
			if err := tx.Save(targetLocked).Error; err != nil {
				return err
			}
		}
		// 换机位：原机位没有别的有效预约时恢复空闲。
		if oldStationID != req.StationID {
			remainCnt, err := s.reservationRepo.CountActiveTx(tx, oldStationID, id)
			if err != nil {
				return err
			}
			if remainCnt == 0 {
				if old := lockedStations[oldStationID]; old.Status == constants.StationReserved {
					old.Status = constants.StationIdle
					if err := tx.Save(old).Error; err != nil {
						return err
					}
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

// lockStationsOrdered 事务内按 ID 升序对一个或多个机位置行锁，返回以机位 ID 为键的映射。
func (s *ReservationService) lockStationsOrdered(tx *gorm.DB, stationIDs ...uint) (map[uint]*model.Station, error) {
	uniq := make(map[uint]struct{}, len(stationIDs))
	ids := make([]uint, 0, len(stationIDs))
	for _, sid := range stationIDs {
		if _, ok := uniq[sid]; ok {
			continue
		}
		uniq[sid] = struct{}{}
		ids = append(ids, sid)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	result := make(map[uint]*model.Station, len(ids))
	for _, sid := range ids {
		station, err := s.stationService.LockForUpdate(tx, sid)
		if err != nil {
			return nil, err
		}
		result[sid] = station
	}
	return result, nil
}

// canReschedule 判断预约当前是否允许改期：仅待确认/已确认，且距开始时间不少于 30 分钟。
func canReschedule(res *model.Reservation, now time.Time) (bool, string) {
	if res.Status != constants.ReservationPending && res.Status != constants.ReservationConfirmed {
		return false, "仅待确认或已确认的预约可以改期"
	}
	if res.StartTime.Before(now.Add(rescheduleLeadTime)) {
		return false, "距开始时间不足 30 分钟，无法改期"
	}
	return true, ""
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

// GetByID 查询预约详情。
func (s *ReservationService) GetByID(id uint) (*model.Reservation, error) {
	return s.getReservation(id)
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
