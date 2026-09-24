<template>
  <div class="reservations-page">
    <van-cell-group inset title="新增预约">
      <van-field v-model="form.station_id" type="number" label="机位ID" placeholder="输入机位ID" />
      <van-field :model-value="form.start_time" label="开始时间" placeholder="选择开始时间" readonly is-link @click="openPicker('createStart')" />
      <van-field :model-value="form.end_time" label="结束时间" placeholder="选择结束时间" readonly is-link @click="openPicker('createEnd')" />
      <van-field v-model="form.remark" label="备注" placeholder="选填" />
    </van-cell-group>
    <div class="submit-btn"><van-button round block type="primary" @click="create">提交预约</van-button></div>

    <van-dropdown-menu>
      <van-dropdown-item v-model="status" :options="statusOptions" @change="load" />
    </van-dropdown-menu>
    <van-cell-group inset title="预约列表">
      <van-cell
        v-for="r in list"
        :key="r.id"
        :title="`预约 #${r.id} · 机位 ${r.station_id}`"
        :label="`${formatTime(r.start_time)} ~ ${formatTime(r.end_time)}`"
        is-link
        @click="openDetail(r)"
      >
        <template #value>
          <StatusBadge kind="reservation" :status="r.status" />
          <van-button
            v-if="['pending', 'confirmed'].includes(r.status)"
            size="mini"
            type="warning"
            plain
            class="op-btn"
            @click.stop="openReschedule(r)"
          >改期</van-button>
          <van-button v-if="isStaffOrAdmin && r.status === 'confirmed'" size="mini" type="primary" class="op-btn" @click.stop="checkIn(r)">开机</van-button>
          <van-button v-if="['pending','confirmed'].includes(r.status)" size="mini" type="danger" plain class="op-btn" @click.stop="cancel(r)">取消</van-button>
        </template>
      </van-cell>
      <EmptyState v-if="!list.length" description="暂无预约" />
    </van-cell-group>
    <van-pagination v-model="page" :total-items="total" :items-per-page="pageSize" @change="load" />

    <!-- 预约详情：改期后重新读取，展示最新机位与时段 -->
    <van-popup v-model:show="showDetail" position="bottom" round>
      <div class="detail-popup">
        <h3 class="detail-title">预约详情</h3>
        <template v-if="detail">
          <van-cell-group inset>
            <van-cell title="预约编号" :value="`#${detail.id}`" />
            <van-cell title="机位" :value="`机位 ${detail.station_id}`" />
            <van-cell title="开始时间" :value="formatTime(detail.start_time)" />
            <van-cell title="结束时间" :value="formatTime(detail.end_time)" />
            <van-cell title="状态">
              <template #value><StatusBadge kind="reservation" :status="detail.status" /></template>
            </van-cell>
            <van-cell title="备注" :value="detail.remark || '-'" />
          </van-cell-group>
          <div class="detail-actions">
            <van-button
              v-if="['pending', 'confirmed'].includes(detail.status)"
              round
              block
              type="warning"
              @click="openReschedule(detail)"
            >改期</van-button>
            <van-button
              v-if="['pending', 'confirmed'].includes(detail.status)"
              round
              block
              type="danger"
              plain
              class="detail-btn"
              @click="cancel(detail)"
            >取消预约</van-button>
          </div>
        </template>
      </div>
    </van-popup>

    <!-- 改期：换机位、换时段 -->
    <van-dialog
      v-model:show="showReschedule"
      title="预约改期"
      show-cancel-button
      :before-close="onRescheduleBeforeClose"
      confirm-button-text="确认改期"
    >
      <van-form>
        <van-cell-group inset>
          <van-field v-model="rescheduleForm.station_id" type="number" label="新机位ID" placeholder="输入目标机位ID" />
          <van-field :model-value="rescheduleForm.start_time" label="新开始时间" placeholder="选择开始时间" readonly is-link @click="openPicker('rescheduleStart')" />
          <van-field :model-value="rescheduleForm.end_time" label="新结束时间" placeholder="选择结束时间" readonly is-link @click="openPicker('rescheduleEnd')" />
        </van-cell-group>
        <p class="reschedule-tip">开始前 30 分钟以上可改期；目标机位时段冲突时将提示冲突，原预约保留。</p>
      </van-form>
    </van-dialog>

    <!-- 日期 + 时间两级选择，新增/改期复用 -->
    <van-popup v-model:show="showPicker" position="bottom" round teleport="body">
      <van-picker-group
        title="选择时间"
        :tabs="['选择日期', '选择时间']"
        next-step-text="下一步"
        @confirm="onPickerConfirm"
        @cancel="showPicker = false"
      >
        <van-date-picker v-model="pickerDate" :min-date="minDate" :max-date="maxDate" />
        <van-time-picker v-model="pickerTime" :columns-type="['hour', 'minute']" />
      </van-picker-group>
    </van-popup>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { showSuccessToast, showToast } from 'vant'
import StatusBadge from '@/components/StatusBadge.vue'
import EmptyState from '@/components/EmptyState.vue'
import {
  listReservations,
  createReservation,
  getReservation,
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

// 详情
const showDetail = ref(false)
const detail = ref<Reservation | null>(null)

// 改期
const showReschedule = ref(false)
const rescheduleForm = ref({ id: 0, station_id: '', start_time: '', end_time: '' })

// 日期+时间选择器（新增与改期共用）
const showPicker = ref(false)
const pickerTarget = ref<'createStart' | 'createEnd' | 'rescheduleStart' | 'rescheduleEnd'>('createStart')
const now = new Date()
const minDate = new Date(now.getFullYear(), now.getMonth(), now.getDate())
const maxDate = new Date(now.getFullYear() + 1, 11, 31)
const pickerDate = ref<string[]>([
  String(now.getFullYear()),
  String(now.getMonth() + 1).padStart(2, '0'),
  String(now.getDate()).padStart(2, '0'),
])
const pickerTime = ref<string[]>(['10', '00'])

async function load() {
  const data = await listReservations({ page: page.value, page_size: pageSize, status: status.value || undefined })
  list.value = data.list
  total.value = data.total
}

function openPicker(target: typeof pickerTarget.value) {
  pickerTarget.value = target
  const current = target.startsWith('create') ? form.value : rescheduleForm.value
  const value = target.endsWith('Start') ? current.start_time : current.end_time
  if (value) {
    const d = new Date(value.replace(/-/g, '/'))
    if (!Number.isNaN(d.getTime())) {
      pickerDate.value = [
        String(d.getFullYear()),
        String(d.getMonth() + 1).padStart(2, '0'),
        String(d.getDate()).padStart(2, '0'),
      ]
      pickerTime.value = [String(d.getHours()).padStart(2, '0'), String(d.getMinutes()).padStart(2, '0')]
    }
  } else {
    pickerTime.value = target.endsWith('Start') ? ['10', '00'] : ['12', '00']
  }
  showPicker.value = true
}

function onPickerConfirm({ selectedValues }: { selectedValues: string[][] }) {
  const [dateParts, timeParts] = selectedValues
  const value = `${dateParts.join('-')} ${timeParts.join(':')}:00`
  switch (pickerTarget.value) {
    case 'createStart':
      form.value.start_time = value
      break
    case 'createEnd':
      form.value.end_time = value
      break
    case 'rescheduleStart':
      rescheduleForm.value.start_time = value
      break
    case 'rescheduleEnd':
      rescheduleForm.value.end_time = value
      break
  }
  showPicker.value = false
}

async function create() {
  const stationId = Number(form.value.station_id)
  if (!stationId || !form.value.start_time || !form.value.end_time) {
    showToast('请填写机位ID与起止时间')
    return
  }
  await createReservation({
    station_id: stationId,
    start_time: toRFC3339(form.value.start_time),
    end_time: toRFC3339(form.value.end_time),
    remark: form.value.remark,
  })
  showSuccessToast('预约成功')
  form.value = { station_id: '', start_time: '', end_time: '', remark: '' }
  load()
}

async function openDetail(r: Reservation) {
  // 重新读取详情，确保改期后拿到最新的机位与时段。
  const fresh = await getReservation(r.id)
  detail.value = fresh
  showDetail.value = true
}

function openReschedule(r: Reservation) {
  showDetail.value = false
  rescheduleForm.value = {
    id: r.id,
    station_id: String(r.station_id),
    start_time: formatTime(r.start_time),
    end_time: formatTime(r.end_time),
  }
  showReschedule.value = true
}

async function onRescheduleBeforeClose(action: string): Promise<boolean> {
  if (action !== 'confirm') return true
  const stationId = Number(rescheduleForm.value.station_id)
  if (!stationId || !rescheduleForm.value.start_time || !rescheduleForm.value.end_time) {
    showToast('请填写新机位ID与新起止时间')
    return false
  }
  try {
    await rescheduleReservation(rescheduleForm.value.id, {
      station_id: stationId,
      start_time: toRFC3339(rescheduleForm.value.start_time),
      end_time: toRFC3339(rescheduleForm.value.end_time),
    })
    showSuccessToast('改期成功')
    load()
    return true
  } catch {
    // 冲突等错误提示已由请求拦截器弹出，原预约保持不变。
    return false
  }
}

async function cancel(r: Reservation) {
  await cancelReservation(r.id)
  showSuccessToast('已取消')
  showDetail.value = false
  detail.value = null
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
.detail-popup { padding: 16px 0 24px; }
.detail-title { text-align: center; margin: 0 0 12px; }
.detail-actions { padding: 16px; }
.detail-btn { margin-top: 10px; }
.reschedule-tip { padding: 8px 24px 4px; color: #969799; font-size: 12px; line-height: 1.6; }
</style>
