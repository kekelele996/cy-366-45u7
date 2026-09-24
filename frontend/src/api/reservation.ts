import { get, post } from '@/utils/request'

export interface Reservation {
  id: number
  user_id: number
  station_id: number
  start_time: string
  end_time: string
  status: string
  remark: string
}

export function listReservations(params: { page: number; page_size: number; status?: string; user_id?: number }) {
  return get<{ list: Reservation[]; total: number }>('/reservations', params)
}

export function getReservation(id: number) {
  return get<Reservation>(`/reservations/${id}`)
}

export function createReservation(data: { station_id: number; start_time: string; end_time: string; remark?: string }) {
  return post<Reservation>('/reservations', data)
}

export function rescheduleReservation(id: number, data: { station_id: number; start_time: string; end_time: string }) {
  return post<Reservation>(`/reservations/${id}/reschedule`, data)
}

export function confirmReservation(id: number) {
  return post<Reservation>(`/reservations/${id}/confirm`)
}

export function cancelReservation(id: number) {
  return post<Reservation>(`/reservations/${id}/cancel`)
}

export function checkInReservation(id: number) {
  return post<Reservation>(`/reservations/${id}/checkin`)
}
