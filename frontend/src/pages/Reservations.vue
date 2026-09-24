<template>
  <div class="reservations-page">
    <van-cell-group inset title="新增预约">
      <van-field v-model="form.station_id" type="number" label="机位ID" placeholder="输入机位ID" />
      <van-field :model-value="form.start_time" label="开始时间" placeholder="如 2026-08-17 10:00:00" @click="showStart = true" readonly />
      <van-field :model-value="form.end_time" label="结束时间" placeholder="如 2026-08-17 12:00:00" @click="showEnd = true" readonly />
      <van-field v-model="form.remark" label="备注" placeholder="选填" />
    </van-cell-group>
    <div class="submit-btn"><van-button round block type="primary" @click="create">提交预约</van-button></div>

    <van-dropdown-menu>
      <van-dropdown-item v-model="status" :options="statusOptions" @change="load" />
    </van-dropdown-menu>
    <van-cell-group inset title="预约列表">
      <van-cell v-for="r in list" :key="r.id" :title="`预约 #${r.id} · 机位 ${r.station_id}`" :label="`${formatTime(r.start_time)} ~ ${formatTime(r.end_time)}`" is-link @click="openDetail(r)">
        <template #value>
          <StatusBadge kind="reservation" :status="r.status" />
          <van-button v-if="['pending','confirmed'].includes(r.status)" size="mini" type="warning" plain class="op-btn" @click.stop="openReschedule(r)">改期</van-button>
          <van-button v-if="isStaffOrAdmin && r.status === 'confirmed'" size="mini" type="primary" class="op-btn" @click.stop="checkIn(r)">开机</van-button>
          <van-button v-if="['pending','confirmed'].includes(r.status)" size="mini" type="danger" plain class="op-btn" @click.stop="cancel(r)">取消</van-button>
        </template>
      </van-cell>
    </van-cell-group>
    <van-pagination v-model="page" :total-items="total" :items-per-page="pageSize" @change="load" />

    <van-popup v-model:show="showStart" position="bottom">
      <van-date-picker v-model="startDate" title="选择开始日期" @confirm="onStartDate" @cancel="showStart = false" />
    </van-popup>
    <van-popup v-model:show="showEnd" position="bottom">
      <van-date-picker v-model="endDate" title="选择结束日期" @confirm="onEndDate" @cancel="showEnd = false" />
    </van-popup>

    <van-popup v-model:show="showDetail" position="bottom" round>
      <van-cell-group inset title="预约详情">
        <van-cell title="预约编号" :value="detail ? `#${detail.id}` : '-'" />
        <van-cell title="机位" :value="detail ? String(detail.station_id) : '-'" />
        <van-cell title="开始时间" :value="formatTime(detail?.start_time)" />
        <van-cell title="结束时间" :value="formatTime(detail?.end_time)" />
        <van-cell title="状态">
          <template #value><StatusBadge v-if="detail" kind="reservation" :status="detail.status" /></template>
        </van-cell>
        <van-cell title="备注" :value="detail?.remark || '-'" />
      </van-cell-group>
    </van-popup>

    <van-popup v-model:show="showReschedule" position="bottom" round>
      <van-cell-group inset :title="`预约改期 #${rescheduleTarget?.id ?? ''}`">
        <van-field v-model="rsForm.station_id" type="number" label="目标机位ID" placeholder="输入目标机位ID" />
        <van-field :model-value="rsForm.start_time" label="新开始时间" placeholder="选择开始时间" readonly is-link @click="openRsPicker('start')" />
        <van-field :model-value="rsForm.end_time" label="新结束时间" placeholder="选择结束时间" readonly is-link @click="openRsPicker('end')" />
      </van-cell-group>
      <div class="submit-btn"><van-button round block type="warning" @click="submitReschedule">确认改期</van-button></div>
    </van-popup>
    <van-popup v-model:show="rsShowDate" position="bottom">
      <van-date-picker v-model="rsDate" title="选择日期" @confirm="onRsDate" @cancel="rsShowDate = false" />
    </van-popup>
    <van-popup v-model:show="rsShowTime" position="bottom">
      <van-time-picker v-model="rsTime" title="选择时间" @confirm="onRsTime" @cancel="rsShowTime = false" />
    </van-popup>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { showSuccessToast, showToast } from 'vant'
import StatusBadge from '@/components/StatusBadge.vue'
import {
  listReservations,
  getReservation,
  createReservation,
  rescheduleReservation,
  cancelReservation,
  checkInReservation,
  type Reservation,
} from '@/api/reservation'
import { formatTime, toRFC3339 } from '@/utils/format'
import { useAuth } from '@/hooks/useAuth'

const { isStaffOrAdmin } = useAuth()
const list = ref<Reservation[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 10
const status = ref('')
const statusOptions = [
  { text: '全部状态', value: '' },
  { text: '待确认', value: 'pending' },
  { text: '已确认', value: 'confirmed' },
  { text: '已开机', value: 'checked_in' },
  { text: '已完成', value: 'completed' },
  { text: '已取消', value: 'cancelled' },
]
const form = ref({ station_id: '', start_time: '', end_time: '', remark: '' })
const showStart = ref(false)
const showEnd = ref(false)
const startDate = ref<string[]>([])
const endDate = ref<string[]>([])

const showDetail = ref(false)
const detail = ref<Reservation | null>(null)

const showReschedule = ref(false)
const rescheduleTarget = ref<Reservation | null>(null)
const rsForm = ref({ station_id: '', start_time: '', end_time: '' })
const rsPicker = ref<'start' | 'end'>('start')
const rsShowDate = ref(false)
const rsShowTime = ref(false)
const rsDate = ref<string[]>([])
const rsTime = ref<string[]>(['10', '00'])
const rsPickedDate = ref('')

async function load() {
  const data = await listReservations({ page: page.value, page_size: pageSize, status: status.value || undefined })
  list.value = data.list
  total.value = data.total
}

function onStartDate({ selectedValues }: any) {
  form.value.start_time = `${selectedValues.join('-')} 10:00:00`
  showStart.value = false
}

function onEndDate({ selectedValues }: any) {
  form.value.end_time = `${selectedValues.join('-')} 12:00:00`
  showEnd.value = false
}

async function create() {
  const stationId = Number(form.value.station_id)
  if (!stationId || !form.value.start_time || !form.value.end_time) {
    showToast('请填写机位ID与起止时间')
    return
  }
  await createReservation({ station_id: stationId, start_time: form.value.start_time, end_time: form.value.end_time, remark: form.value.remark })
  showSuccessToast('预约成功')
  form.value = { station_id: '', start_time: '', end_time: '', remark: '' }
  load()
}

async function openDetail(r: Reservation) {
  detail.value = await getReservation(r.id)
  showDetail.value = true
}

function openReschedule(r: Reservation) {
  rescheduleTarget.value = r
  rsForm.value = { station_id: String(r.station_id), start_time: formatTime(r.start_time), end_time: formatTime(r.end_time) }
  showReschedule.value = true
}

function openRsPicker(which: 'start' | 'end') {
  rsPicker.value = which
  rsShowDate.value = true
}

function onRsDate({ selectedValues }: any) {
  rsPickedDate.value = selectedValues.join('-')
  rsShowDate.value = false
  rsShowTime.value = true
}

function onRsTime({ selectedValues }: any) {
  const value = `${rsPickedDate.value} ${selectedValues.join(':')}:00`
  if (rsPicker.value === 'start') {
    rsForm.value.start_time = value
  } else {
    rsForm.value.end_time = value
  }
  rsShowTime.value = false
}

async function submitReschedule() {
  const target = rescheduleTarget.value
  if (!target) return
  const stationId = Number(rsForm.value.station_id)
  if (!stationId || !rsForm.value.start_time || !rsForm.value.end_time) {
    showToast('请填写目标机位与新时段')
    return
  }
  await rescheduleReservation(target.id, {
    station_id: stationId,
    start_time: toRFC3339(rsForm.value.start_time),
    end_time: toRFC3339(rsForm.value.end_time),
  })
  showSuccessToast('改期成功')
  showReschedule.value = false
  load()
}

async function cancel(r: Reservation) {
  await cancelReservation(r.id)
  showSuccessToast('已取消')
  load()
}

async function checkIn(r: Reservation) {
  await checkInReservation(r.id)
  showSuccessToast('开机成功')
  load()
}

onMounted(load)
</script>

<style scoped>
.submit-btn { margin: 12px 16px; }
.op-btn { margin-left: 6px; }
</style>
