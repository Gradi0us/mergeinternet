# DESIGN-v2-mergeinternet-g3-bonding-tunnel

**Created**: 2026-10-19
**Last Updated**: 2026-10-19 — v2: user chốt goal G3; MVP tunnel đã code + test, chưa chạy thật với VPS
**Mode**: Planning
**Level**: L3-Analyze
**Language**: vi
**Status**: 🔄 PARTIAL: code + unit/integration test ✅; chạy thật với VPS + 3 thiết bị ⬜
**Supersedes**: DESIGN-v1 (giả định goal G1 tải file)

---

## Key Findings

| # | Finding | Chi tiết | Bằng chứng | Độ tin cậy |
|---|---------|----------|------------|------------|
| 1 | **Đổi goal G1 → G3 buộc phải đổi kiến trúc** | Striping kiểu plexo không áp dụng được cho một kết nối đơn. Cần tunnel tới VPS và chia ở tầng packet | Quyết định của user (2026-10-19) + DESIGN-v1 §1 | CAO |
| 2 | **Với G3 không cần agent trên điện thoại** | Cả Android và iPhone tether USB đều hiện ra như card mạng trên Windows, nên client chỉ cần gắn socket vào từng card | `mi-client interfaces` liệt kê card; README | CAO |
| 3 | **Gắn socket theo card trên Windows: `IP_UNICAST_IF` = 31, network byte order** | Cùng kỹ thuật wireguard-windows dùng | `wireguard/conn/bind_windows.go` (module cache) | CAO |
| 4 | **Buffer UDP mặc định của Windows làm rơi packet khi có burst** | Stress test: 1/4 lần fail, mọi bộ đếm trong app = 0. Lên 4 MiB thì 0/6 lần fail | `go test ./... -count=4` vs `-count=6` | CAO |
| 5 | **Server restart từng gây dùng lại nonce** | Đã sửa: counter của server bắt đầu từ thời điểm boot (ns); client nhận boot ID qua probe-ack rồi tạo session mới | `TestClientResyncsAfterServerRestart`, `TestServerCounterNeverRestarts` | CAO |

---

## 1. ADR-01: Pivot G1 → G3

- **GOAL (user, 2026-10-19)**: gộp **mọi traffic** của máy Windows, kể cả kết nối đơn luồng; thiết bị gồm Windows, Android và iPhone; viết mới bằng Go.
- **Lọc theo goal**: striping tầng app (plexo) và SOCKS gateway chia theo connection đều **FAIL** goal (một luồng không nhanh hơn). Bonding tầng packet qua VPS **PASS**.
- **Chọn giữa các phương án PASS**:
  - OpenMPTCProuter: cần router OpenWrt và nhiều cấu hình.
  - Speedify: trả phí, đóng mã.
  - **Tự viết tunnel UDP** (đã chọn): đúng yêu cầu "Go viết mới", ít phụ thuộc nhất.
- **Hệ quả**: phần "remote agent" của v1 (S4/S5) bị hoãn. USB tether là đủ cho 3 thiết bị.

## 2. Kiến trúc

```
TUN mi0 ─► Engine.SendPacket ─► seq++ ─► sched.Pick (weighted / redundant)
                                             │
              ┌──────────────────────────────┼──────────────────────────────┐
              ▼                              ▼                              ▼
       UDP sock (Wi-Fi)              UDP sock (Ethernet 3)          UDP sock (Ethernet 4)
       IP_UNICAST_IF                 IP_UNICAST_IF                  IP_UNICAST_IF
              └──────────────► VPS :51820 (1 socket) ◄──────────────────────┘
                                       │ Open → replay check → reorder → TUN → NAT
```

| Thành phần | File | Ghi chú |
|---|---|---|
| Wire format + AEAD | `internal/proto/proto.go` | Header 20B (AAD) + XChaCha20-Poly1305; nonce = session‖dir‖counter |
| Anti-replay | `internal/window/window.go` | RFC 6479, cửa sổ 8128 |
| Reorder | `internal/reorder/reorder.go` | Seq bắt đầu từ 1 mỗi session; bỏ qua lỗ hổng sau `reorder_ms` |
| Scheduler | `internal/sched/sched.go` | Smooth weighted fair (virtual time); đường sống lại bắt đầu ngang hàng với đường khác |
| Engine | `internal/engine/engine.go` | Probe 500ms, đường chết sau 2.5s, session tăng dần, server boot ID |
| Bind | `internal/bindif/` | Windows `IP_UNICAST_IF` + tắt `SIO_UDP_CONNRESET`; Linux `SO_BINDTODEVICE` |
| TUN/route | `internal/tunif/` | wintun; route `0/1` + `128/1` để không xóa default route của các card thật |

**Overhead**: 44B tunnel + 28B IPv4/UDP = 72B mỗi packet. MTU mặc định 1360 nên gói ngoài là 1432B.

## 3. Kiểm chứng (DoD)

| # | Mục | Trạng thái | Bằng chứng |
|---|-----|-----------|-----------|
| 1 | Spec/doc | ✅ | Tài liệu này + README |
| 2 | Code | ✅ | `cmd/`, `internal/` |
| 3 | Unit test | ✅ | proto, window, reorder, sched |
| 4 | Callsite/CLI | ✅ | `mi-client run/interfaces/genkey`, `mi-server run/genkey`; `interfaces` đã chạy trên máy user |
| 5 | Integration | 🔄 | Engine client↔server qua 3 socket UDP thật: thứ tự, failover, khôi phục đường, redundant, client restart, server restart. **Chưa chạy**: TUN thật, route Windows, VPS, tether thật |

Chưa chạy được: `-race` (máy không có cgo), test trên Linux.

## 4. Rủi ro và việc tiếp theo

| # | Rủi ro / việc | Ưu tiên |
|---|---|---|
| R1 | Chạy thật lần đầu theo checklist trong README, thu log | P0 |
| R2 | Trọng số tĩnh: 4G dao động nên đường chậm bị quá tải. **Tiếp theo**: tự ước lượng băng thông mỗi đường (đo tốc độ nhận ở phía nhận, gửi phản hồi trong probe) | P1 |
| R3 | IPv6 rò qua card 4G (bỏ qua tunnel). Hiện xử lý bằng tắt IPv6 thủ công; có thể thêm chặn IPv6 tự động | P1 |
| R4 | `reorder_ms` cố định. Có thể tự tính từ chênh lệch RTT giữa các đường | P2 |
| R5 | Một client mỗi server; chưa có pairing hay xoay vòng key | P2 |
| R6 | Agent cho thiết bị không cắm USB được (v1 S5) | P3 |

## Document Lineage

| Version | Document | Focus | Status |
|---------|----------|-------|--------|
| v1 | DESIGN-v1-mergeinternet-multidevice-plexo-inspired.md | Phân tích plexo, goal G1 | Superseded (goal → G3) |
| **v2** | **DESIGN-v2-mergeinternet-g3-bonding-tunnel.md** | **Tunnel bonding G3, MVP Go** | ✅ **Current** |
