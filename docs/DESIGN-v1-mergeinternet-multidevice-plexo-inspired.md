# DESIGN-v1-mergeinternet-multidevice-plexo-inspired

**Created**: 2026-10-19
**Last Updated**: 2026-10-19 — v1: phân tích plexo + thiết kế ý tưởng hệ thống gộp mạng từ 3 thiết bị
**Research Target**: https://github.com/anmolkapil/plexo → tự build hệ thống tương tự, mở rộng ra ≥3 thiết bị
**Mode**: Planning
**Level**: L3-Analyze (rút gọn vì quota session có hạn)
**Language**: vi
**Context**: README + package.json + `src/main/network/deviceBinding.ts` của plexo; 1 web search
**Status**: Draft ý tưởng — chờ user chốt GOAL (xem §1)
**Researcher**: Claude Agent (deep-research skill)

---

## Key Findings

| # | Finding | Chi tiết | Bằng chứng | Độ tin cậy |
|---|---------|----------|------------|------------|
| 1 | **Plexo không "bond" mạng, nó chia nhỏ ở tầng ứng dụng** | Chia 1 file thành nhiều chunk qua HTTP Range, mỗi chunk đi 1 card mạng; torrent thì chia peer theo card mạng. Không cần VPN hay card mạng ảo | README plexo | CAO |
| 2 | **Chỉ tăng tốc khi mỗi đường có đường ra internet RIÊNG** | Hai card mạng dùng chung một router thì không nhanh hơn. Đây là ràng buộc vật lý, không phải giới hạn của code | README: "each network needs its own internet path" | CAO |
| 3 | **Plexo yêu cầu mọi nguồn mạng hiện ra như card mạng trên 1 máy** | Điện thoại phải cắm USB tether. Với 3+ thiết bị sẽ vướng số cổng USB, driver Windows và sạc pin | README (TetherKit, driver Windows) | CAO |
| 4 | **Gắn socket vào card mạng chỉ chắc chắn trên Linux** | Dùng `SO_BINDTODEVICE` qua koffi FFI. macOS/Windows chỉ dùng `localAddress`, nên Windows có thể đẩy traffic ra card khác | `deviceBinding.ts` + README limitations | CAO |
| 5 | **Muốn dùng 3 thiết bị thì cần abstraction "Route" rộng hơn card mạng** | Route = card mạng cục bộ HOẶC proxy/agent chạy trên thiết bị khác trong LAN. Đây là điểm khác biệt chính so với plexo | Suy luận thiết kế từ #3 | TRUNG BÌNH |
| 6 | **Gộp toàn bộ traffic (mọi app) là bài toán khác, cần VPS** | MPTCP/OpenMPTCProuter/Speedify cần server tổng hợp. Một luồng TCP đơn sẽ bị out-of-order khi chia qua nhiều đường | Speedify blog, TidBITS 2021 | TRUNG BÌNH |

---

## Table of Contents

1. [GOAL — cần chốt trước](#1-goal--cần-chốt-trước)
2. [Plexo hoạt động thế nào](#2-plexo-hoạt-động-thế-nào)
3. [Chọn nguồn kết nối cho 3 thiết bị](#3-chọn-nguồn-kết-nối-cho-3-thiết-bị)
4. [Kiến trúc đề xuất](#4-kiến-trúc-đề-xuất)
5. [Giao thức Agent](#5-giao-thức-agent)
6. [Tính năng — plexo có gì, ta thêm gì](#6-tính-năng--plexo-có-gì-ta-thêm-gì)
7. [Fermi — tốc độ và dữ liệu 4G thực tế](#7-fermi--tốc-độ-và-dữ-liệu-4g-thực-tế)
8. [Tech stack + RICE](#8-tech-stack--rice)
9. [Roadmap](#9-roadmap)
10. [Inversion, góc nhìn, rủi ro](#10-inversion-góc-nhìn-rủi-ro)
11. [Câu hỏi mở cho user](#11-câu-hỏi-mở-cho-user)
12. [Kết luận + Synthesis Diagram](#12-kết-luận--synthesis-diagram)

---

## 1. GOAL — cần chốt trước

Theo RULE "Goal-Frame Before Effort-Optimize", kiến trúc sẽ rẽ hướng hoàn toàn tùy goal:

```
                       GOAL LÀ GÌ?
                           │
        ┌──────────────────┼──────────────────────┐
        ▼                  ▼                      ▼
  G1: Tải file/torrent   G2: Mọi app nhiều       G3: MỌI traffic kể cả
  nhanh hơn (như plexo)  kết nối (browser,       1 luồng (video call,
                         Steam) chạy nhanh hơn   game, upload 1 stream)
        │                  │                      │
        ▼                  ▼                      ▼
  App-level striping     Local SOCKS gateway      Bonding thật: MPTCP /
  (KHÔNG cần server)     chia CONNECTION theo     tunnel + VPS tổng hợp
                         route (không cần server) (CẦN VPS, tốn phí, có latency)
  Effort: thấp-TB        Effort: TB               Effort: cao
```

**Giả định của tài liệu này**: G1 là lõi, G2 là tính năng mở rộng, G3 để sau (Phase 4, tùy chọn). Nếu goal thật của bạn là G3 thì cần làm lại §4. Xem §11.

---

## 2. Plexo hoạt động thế nào

### 2.1 Tổng quan

```
┌──────────────────── PLEXO (Electron + TypeScript, MIT) ─────────────────────┐
│                                                                             │
│  Browser Extension ──link/cookie──►  MAIN PROCESS (Node)                    │
│  (Chrome/Firefox)                     ├─ download/  HTTP Range engine       │
│                                       ├─ network/   interfaces.ts           │
│                                       │             deviceBinding.ts        │
│                                       │             routes.ts, limits.ts    │
│                                       └─ webtorrent 3.0.21 (torrent)        │
│                                                 │                           │
│                    ┌────────────────────────────┼──────────────┐            │
│                    ▼                            ▼              ▼            │
│              [Wi-Fi]                     [Ethernet]     [USB tether phone]  │
│                    │                            │              │            │
└────────────────────┼────────────────────────────┼──────────────┼────────────┘
                     ▼                            ▼              ▼
                  ISP A                        ISP B          Mạng di động C
```

### 2.2 Thuật toán chia tải (theo README)

```
File 1 GB ──► chia thành chunk ──► HÀNG ĐỢI CHUNG
                                       │
          ┌────────────────────────────┼────────────────────────┐
          ▼                            ▼                        ▼
   Route A (nhanh)              Route B (vừa)             Route C (chậm)
   lấy chunk liên tục           lấy ít hơn                lấy ít nhất
          │                            │                        │
          └──────────── CUỐI FILE: "racing" ────────────────────┘
             route rảnh tải lại chunk đang chậm; bản nào xong trước thì thắng
```

- Đây là work-stealing: route nhanh tự lấy nhiều chunk hơn, không cần chia tỉ lệ cứng.
- Server không hỗ trợ Range thì quay về 1 luồng.
- Torrent: peer mới được gán vào route đang có ít peer nhất.

### 2.3 Chi tiết kỹ thuật đáng học (CONFIG-BASED, `deviceBinding.ts`)

| Hệ điều hành | Cách plexo gắn socket | Vấn đề |
|-----|-----|-----|
| Linux | `SO_BINDTODEVICE` (=25, `SOL_SOCKET`=1) qua **koffi** gọi `libc.so.6`. Không cần root từ kernel 5.7 | Chắc chắn |
| macOS | `localAddress` | Thường ổn |
| Windows | `localAddress` | README thừa nhận Windows có thể đi ra card khác |

**Bài học cho bản tự build**: trên Windows nên thêm `IP_UNICAST_IF` (ws2_32 `setsockopt`, IPPROTO_IP), trên macOS thêm `IP_BOUND_IF`, cùng cách gọi FFI. Các hằng số này **chưa kiểm chứng trong session này**; cần tra docs Microsoft/Apple khi code.

---

## 3. Chọn nguồn kết nối cho 3 thiết bị

### 3.1 Bảy cách đưa một nguồn mạng về máy chính (MECE)

| ID | Cách nối | Tốc độ thực tế | Cần code trên thiết bị phụ? | Ưu | Nhược |
|----|----------|----------------|------------------------------|----|-------|
| **S1** | **USB tether** (điện thoại → PC) | Đủ cho 4G/5G | Không | Ổn định, sạc luôn, plexo đã hỗ trợ | Giới hạn cổng USB; Android trên macOS cần TetherKit; driver Windows |
| **S2** | **Ethernet** (modem cáp quang / router 4G riêng) | Cao nhất | Không | Ổn định nhất | Cần cổng LAN; dùng USB-LAN adapter nếu thiếu |
| **S3** | **Wi-Fi hotspot** của điện thoại → **USB Wi-Fi dongle thứ 2** trên PC | Hotspot thường vài chục đến ~100+ Mbps | Không | Không dây | Mỗi hotspot cần 1 card Wi-Fi riêng trên PC; nóng máy, hao pin |
| **S4** | **SOCKS5/HTTP proxy chạy trên điện thoại**, cùng Wi-Fi LAN nhà | Bị giới hạn bởi Wi-Fi LAN | App proxy có sẵn | Không cáp, mở rộng được nhiều thiết bị | ⚠️ Điện thoại vào Wi-Fi nhà sẽ ưu tiên đi internet qua Wi-Fi, nên **phải ép đi qua 4G** (xem 3.3) |
| **S5** | **Agent tự viết** trên thiết bị phụ (điện thoại/laptop) | Như S4 | Có (app riêng) | Kiểm soát hoàn toàn: đo tốc độ, pin, quota, bảo mật | Tốn công nhất; iOS gần như không chạy nền được |
| **S6** | Bluetooth PAN | Rất chậm | Không | — | Chỉ dùng làm kênh điều khiển/failover, **loại** cho tải dữ liệu |
| **S7** | Tunnel + VPS (MPTCP/OpenMPTCProuter/Speedify) | Tổng các đường trừ overhead | Có / router riêng | Gộp được mọi app, cả 1 luồng | Cần VPS, tăng latency, tốn phí; là bài toán G3 |

### 3.2 Topology đề xuất cho 3 thiết bị

```
                 ┌───────────────────────────────────────────┐
                 │   THIẾT BỊ A — COORDINATOR (PC/Laptop)    │
                 │   chạy app chính + scheduler + UI         │
                 └───┬───────────────┬───────────────────┬───┘
       Route 1       │     Route 2   │          Route 3  │
       (interface)   │  (interface)  │       (agent/proxy, qua LAN)
                     │               │                   │
              Ethernet│        USB    │            Wi-Fi LAN nhà
                     ▼        tether ▼                   ▼
             ┌────────────┐  ┌──────────────┐   ┌───────────────────────┐
             │ Router nhà │  │ THIẾT BỊ B   │   │ THIẾT BỊ C            │
             │ (cáp quang │  │ Android #1   │   │ Android #2 / laptop 2 │
             │  ISP 1)    │  │ SIM nhà mạng X│   │ SIM nhà mạng Y        │
             └─────┬──────┘  └──────┬───────┘   │ Agent: nhận lệnh qua  │
                   │                │           │ Wi-Fi, tải qua 4G     │
                   ▼                ▼           └──────────┬────────────┘
                ISP 1            Nhà mạng X               ▼
                                                      Nhà mạng Y
   ★ 3 upstream KHÁC NHAU (ISP 1 / X / Y) → mới có lợi tốc độ
```

**Vì sao chọn cấu hình này**: B cắm USB (S1) vì đó là đường tốt nhất, không cần code. C đi qua LAN (S4→S5) để không phụ thuộc cổng USB và cho phép thêm thiết bị thứ 4, thứ 5 sau này. **Nên dùng 2 nhà mạng khác nhau**: hai SIM cùng nhà mạng cùng trạm phát có thể tranh nhau băng thông cell (giả thuyết, chưa đo).

### 3.3 Bẫy quan trọng: điện thoại trong LAN vẫn đi internet qua Wi-Fi

```
SAI:  PC ──LAN──► Phone C ──(Android chọn Wi-Fi làm default)──► Router nhà ──► ISP 1
      → Route 3 thực chất là ISP 1 → KHÔNG tăng tốc (vi phạm Finding #2)

ĐÚNG: PC ──LAN──► Phone C ──(socket ép bind CELLULAR)──► Nhà mạng Y
```

Cách ép trên Android (cho agent tự viết): `ConnectivityManager.requestNetwork()` với `NetworkCapabilities.TRANSPORT_CELLULAR`, rồi dùng `Network.bindSocket()` hoặc `Network.openConnection()` cho traffic tải về. Có thể bật thêm Developer options "Mobile data always active". **Tính năng phải có**: Coordinator tự kiểm tra **public IP / ASN của từng route** để phát hiện hai route trùng upstream (xem §6, F-N1).

iOS: app chạy nền bị hạn chế nặng, nên **chỉ dùng iPhone qua USB tether (S1) hoặc hotspot (S3)**, không làm agent.

---

## 4. Kiến trúc đề xuất

### 4.1 Abstraction cốt lõi: `Route`

Plexo coi "network = card mạng". Bản mới tổng quát hóa thành:

```ts
type Route =
  | { kind: "interface"; ifName: string; localAddr: string }          // S1, S2, S3
  | { kind: "socks";     host: string; port: number; auth?: Creds }   // S4
  | { kind: "agent";     deviceId: string; endpoint: string; key: PubKey } // S5

interface RouteTransport {
  fetchRange(req: RangeRequest, signal: AbortSignal): AsyncIterable<Uint8Array>;
  openTcp(host: string, port: number): Promise<Duplex>;   // cho torrent + G2 gateway
  probe(): Promise<RouteHealth>; // RTT, throughput, public IP, ASN
}
```

Scheduler, torrent engine và gateway **chỉ biết `RouteTransport`**, không cần biết route đó là USB, proxy hay agent. Đây là điểm mở rộng chính so với plexo.

### 4.2 Component map (MECE)

```
┌═══════════════════════ COORDINATOR (thiết bị A) ═════════════════════════┐
║                                                                          ║
║  INPUT              CORE                         OUTPUT                  ║
║  ─────              ────                         ──────                  ║
║  ┌───────────┐   ┌───────────────────────┐    ┌──────────────────────┐   ║
║  │ UI desktop│──►│ Job Manager           │───►│ Storage Writer       │   ║
║  │ Extension │   │ (queue, pause/resume, │    │ preallocate, ghi theo│   ║
║  │ Web UI    │   │  schedule, persist)   │    │ vị trí, hash verify  │   ║
║  │ (mobile)  │   └──────────┬────────────┘    └──────────────────────┘   ║
║  └───────────┘              ▼                                            ║
║               ┌──────────────────────────┐   ┌──────────────────────┐    ║
║               │ Chunk Scheduler          │◄──│ Policy Engine        │    ║
║               │ work-stealing, adaptive  │   │ quota/giá per route, │    ║
║               │ chunk size, endgame race │   │ pin, giờ, ưu tiên    │    ║
║               └──────────┬───────────────┘   └──────────────────────┘    ║
║                          ▼                                               ║
║               ┌──────────────────────────┐   ┌──────────────────────┐    ║
║               │ Route Registry           │◄──│ Health Prober        │    ║
║               │ discover interface +     │   │ RTT, Mbps, public IP,│    ║
║               │ paired agent + proxy     │   │ ASN → dedup upstream │    ║
║               └──────────┬───────────────┘   └──────────────────────┘    ║
║            ┌─────────────┼─────────────┐                                 ║
║            ▼             ▼             ▼                                 ║
║    InterfaceTransport SocksTransport AgentTransport                      ║
║    (bind per OS)      (SOCKS5)       (QUIC/WSS + Noise/TLS)              ║
╚══════════╪═════════════════╪═══════════════╪═════════════════════════════╝
           ▼                 ▼               ▼
        NIC/USB         Phone proxy app   AGENT (thiết bị B/C/…)
                                          ├─ Pairing (QR)
                                          ├─ Cellular binder (Android)
                                          ├─ Range fetcher / TCP relay
                                          └─ Telemetry: pin, nhiệt, quota
```

### 4.3 Luồng tải 1 file qua 3 route

```
 UI        JobMgr     Scheduler    Route1(ETH)  Route2(USB)  Route3(Agent C)  Disk
  │ add URL  │            │            │            │             │           │
  │─────────►│ HEAD/Range?│            │            │             │           │
  │          │───────────►│ probe size, accept-ranges              │           │
  │          │            │─ chunk#1 ─►│            │             │           │
  │          │            │─ chunk#2 ────────────────►            │           │
  │          │            │─ chunk#3 ───────────────────────────────►          │
  │          │            │◄── bytes ──│            │             │           │
  │          │            │────────────────────────────── write@offset ──────►│
  │          │            │ (route xong trước → lấy chunk tiếp: work-stealing) │
  │          │            │ endgame: chunk#N chậm ở R3 → R1 race lại          │
  │          │◄── done ───│ verify size / hash                                │
  │◄─ notify─│            │                                                   │
```

---

## 5. Giao thức Agent

### 5.1 Pairing và bảo mật

```
Coordinator hiển thị QR = { lanIp, port, coordinatorPubKey, oneTimeToken }
        │
Agent quét QR → handshake (Noise XX hoặc TLS + pin public key) → lưu khóa
        │
Từ đó: chỉ Coordinator đã pair mới gửi lệnh được
```

⚠️ **Cookie/session của trình duyệt** (tính năng "authenticated session" của plexo) sẽ được gửi sang agent để tải hộ. Vì vậy kênh **bắt buộc mã hóa**, agent **không ghi log header**, và có allowlist domain.

### 5.2 Message (control: JSON qua WSS hoặc QUIC stream; data: luồng byte thô)

| Message | Hướng | Nội dung |
|---------|-------|----------|
| `HELLO` | A→C | version, capabilities |
| `STATUS` | C→A | battery %, nhiệt, loại mạng (5G/4G), quota còn lại, public IP |
| `FETCH_RANGE` | A→C | `{jobId, chunkId, url, headers, start, end}` |
| `DATA` | C→A | stream byte của chunk (QUIC stream riêng cho mỗi chunk) |
| `CANCEL` | A→C | chunkId (dùng khi racing thắng ở route khác) |
| `TCP_OPEN` | A→C | host:port (cho torrent peer và gateway G2) |
| `SPEEDTEST` | A→C | đo throughput tới endpoint chuẩn |

**Lựa chọn transport**: QUIC (Rust `quinn` / Go `quic-go`) cho mỗi chunk một stream, không bị head-of-line blocking. Phương án đơn giản hơn cho MVP: HTTP/1.1 trên TLS, mỗi chunk một request, vì agent chỉ cần trả `GET /fetch?...`.

---

## 6. Tính năng — plexo có gì, ta thêm gì

### 6.1 Kế thừa từ plexo (baseline)

Đa mạng (bật/tắt, đặt tên, màu), chia chunk cân bằng, tự chỉnh số stream, racing chunk chậm, tốc độ live theo route; extension trình duyệt; direct/magnet/torrent, pause/resume qua restart; giới hạn băng thông global/per-route, Slow mode, quota dữ liệu, lịch tải.

### 6.2 Tính năng MỚI đề xuất (MoSCoW)

| ID | Tính năng | Mô tả | MoSCoW |
|----|-----------|-------|--------|
| **F-N1** | **Upstream Dedup Detector** | Probe public IP + ASN cho từng route. Hai route trùng upstream thì cảnh báo và gộp làm một | **Must** |
| **F-N2** | **Remote Route (Agent/SOCKS)** | Dùng thiết bị trong LAN làm route (§3.1 S4/S5); đây là lý do có tính năng "3 thiết bị" | **Must** |
| **F-N3** | **Cost-aware scheduling** | Mỗi route có giá/GB và quota/tháng. Ưu tiên cáp quang không giới hạn, chỉ dùng 4G khi cần tốc độ hoặc khi file đủ lớn | **Must** |
| **F-N4** | **Device health guard** | Agent báo pin/nhiệt. Pin <20% hoặc máy quá nóng thì giảm hoặc ngưng route đó | Should |
| **F-N5** | **Windows strict binding** | `IP_UNICAST_IF` + kiểm tra route table, sửa đúng điểm yếu README plexo thừa nhận | Should |
| **F-N6** | **Local Multi-route Gateway (G2)** | SOCKS5/HTTP proxy cục bộ, mỗi TCP connection gán một route (least-load/RTT). Trình duyệt, Steam, `aria2` được hưởng lợi mà không cần VPS | Should |
| **F-N7** | **Mobile Web UI** | Thêm link, theo dõi tiến độ từ điện thoại | Could |
| **F-N8** | **HLS/DASH segment fetch** | Tải song song segment video qua nhiều route (tích hợp yt-dlp làm extractor) | Could |
| **F-N9** | **Multi-route upload** | Upload multipart (S3, resumable Google Drive) chia part theo route | Could |
| **F-N10** | **Per-route analytics** | Lịch sử Mbps, GB đã dùng theo ngày, cảnh báo sắp hết quota | Could |
| **F-N11** | **Failover routing** | Một route rớt thì chunk đang chạy được chuyển ngay sang route khác. Sửa luôn lỗi README nêu: route reconnect phải pause/resume mới chạy lại | Should |
| **F-N12** | **Full bonding mode (G3)** | Tunnel tới VPS (MPTCP / WireGuard đa đường), gộp mọi traffic | Won't (v1) |

---

## 7. Fermi — tốc độ và dữ liệu 4G thực tế

**Giả định** (thay bằng số đo thật ở Phase 0):

```
Route 1 Cáp quang   : 100 Mbps (range 80–300)
Route 2 4G/5G SIM X :  40 Mbps (range 10–150, dao động mạnh theo giờ/vị trí)
Route 3 4G/5G SIM Y :  30 Mbps (range 10–150)
Hiệu suất striping  : ~85–90% (overhead: TCP slow-start, racing, chunk cuối)

Tổng lý thuyết  = 170 Mbps
Thực tế         ≈ 170 × 0.875 ≈ 150 Mbps   (range ~110–160)
Speedup so với chỉ cáp quang ≈ 1.5×

File 10 GB:  chỉ cáp quang ≈ 10×8000/100 ≈ 800 s (13.3 phút)
             3 route       ≈ 80000/150   ≈ 533 s ( 8.9 phút)

Data 4G tiêu tốn (tỉ lệ băng thông): (40+30)/170 ≈ 41% → ~4.1 GB / file 10 GB
```

**Sanity check**: tiết kiệm khoảng 4.5 phút nhưng tốn khoảng 4 GB data di động. Đây là lý do **F-N3 cost-aware là Must**: nếu không có nó, quota 4G sẽ cạn rất nhanh.

**Băng thông LAN cho route agent**: route 3 đi qua Wi-Fi nhà, Wi-Fi 5 thực tế thường vài trăm Mbps, dư cho 30–150 Mbps của 4G. Nếu dùng 5+ điện thoại cùng Wi-Fi thì LAN Wi-Fi sẽ thành nút cổ chai (ước lượng, chưa đo).

---

## 8. Tech stack + RICE

### 8.1 Phương án

| ID | Coordinator | Agent | Ưu | Nhược |
|----|-------------|-------|----|-------|
| **T1** | **Fork plexo** (Electron+TS, MIT) + thêm `SocksTransport`/`AgentTransport` | Android Kotlin | Có sẵn engine, torrent, extension, UI; chỉ thêm abstraction Route | Phải hiểu codebase người khác; Electron nặng |
| **T2** | Viết mới bằng **Go** (daemon) + Web UI | Go (desktop agent) + Kotlin (Android) | 1 binary, cross-compile dễ, `quic-go`, socket control tốt | Torrent engine Go (`anacrolix/torrent`) phải tích hợp lại; tự làm UI |
| **T3** | **Rust + Tauri** | Rust core + Kotlin shell | Hiệu năng, binary nhẹ, `quinn` QUIC | Learning curve cao nhất |
| **T4** | Không code: plexo + USB tether hết 3 thiết bị | — | 0 công | Vướng cổng USB/driver, không có F-N1..N4 |

### 8.2 RICE (Reach = số kịch bản dùng được /10; Impact 1–5; Effort = person-week)

| ID | Reach | Impact | Confidence | Effort | RICE |
|----|-------|--------|------------|--------|------|
| T4 | 4 | 2 | 95% | 0.1 | **76** (dùng để validate, không phải đích) |
| T1 | 8 | 4 | 70% | 4 | **5.6** ★ |
| T2 | 9 | 4 | 60% | 8 | 2.7 |
| T3 | 9 | 4 | 50% | 12 | 1.5 |

**Khuyến nghị**: **T4 trước (Phase 0, để đo) → T1 (fork plexo, thêm Route abstraction)**. Chỉ chuyển sang T2 nếu codebase plexo cản trở việc thêm Remote Route. Đây là quyết định Type 2, đảo lại được. Agent Android viết bằng Kotlin ở mọi phương án vì cần API `ConnectivityManager`.

---

## 9. Roadmap

```
Phase 0 — VALIDATE (0.5 ngày, 0 code)          ★ làm trước (chống AP2)
  □ Cài plexo, USB tether 2 điện thoại (2 nhà mạng) + cáp quang
  □ Đo: Mbps từng route, tổng thực tế, % data 4G → thay số §7
  □ Gate: tổng < 1.3× route tốt nhất → dừng, xem lại giả định

Phase 1 — REMOTE ROUTE MVP (1–2 tuần)
  □ Fork plexo, tách RouteTransport interface
  □ SocksTransport (dùng app proxy có sẵn trên Android, ép cellular)
  □ F-N1 Upstream Dedup Detector
  □ DoD: tải 1 file qua 3 route, UI hiện 3 route với 3 public IP khác nhau

Phase 2 — AGENT RIÊNG (2–3 tuần)
  □ Android agent Kotlin: pairing QR, bind cellular, FETCH_RANGE, STATUS
  □ AgentTransport (HTTPS hoặc QUIC), mã hóa + allowlist
  □ F-N3 cost-aware, F-N4 health guard, F-N11 failover

Phase 3 — MỞ RỘNG (tùy chọn)
  □ F-N6 Local Multi-route Gateway (G2), F-N5 Windows strict binding
  □ F-N7 Mobile Web UI, F-N8 HLS, F-N10 analytics

Phase 4 — BONDING TOÀN HỆ THỐNG (chỉ nếu goal = G3)
  □ Đánh giá OpenMPTCProuter + VPS thay vì tự viết
```

---

## 10. Inversion, góc nhìn, rủi ro

### 10.1 Inversion

| # | Giả định sai | Lật ngược | Chân lý |
|---|-------------|-----------|---------|
| 1 | "Thêm thiết bị thì nhanh hơn" | Plexo README: card mạng dùng chung upstream thì không tăng tốc | Chỉ thêm **upstream khác**, không phải thêm thiết bị. Từ đó ra F-N1 |
| 2 | "Gộp mạng thì mọi thứ nhanh hơn" | Striping cần Range hoặc nhiều kết nối; một luồng TCP không thể chia (TidBITS 2021 về out-of-order) | Video call, game 1 luồng KHÔNG nhanh hơn nếu không có VPS bonding |
| 3 | "Bind localAddress là đủ" | `deviceBinding.ts` ghi chú: chỉ bind localAddress thì packet vẫn ra default route; README nêu Windows lệch card | Cần bind chặt theo OS (F-N5) |
| 4 | "Điện thoại cùng Wi-Fi làm route được ngay" | Android ưu tiên Wi-Fi làm default network | Phải ép cellular, nếu không thì route 3 thực chất là cáp quang |

### 10.2 Góc nhìn (UC5)

| Perspective | Mối quan tâm chính | Verdict |
|-------------|-------------------|---------|
| Engineering | Abstraction Route sạch; binding đúng từng OS | Khả thi với T1, rủi ro nằm ở Windows |
| Security | Cookie/session gửi sang agent; agent mở cổng trên LAN | Bắt buộc pairing + mã hóa + allowlist |
| Ops/User | Pin và nhiệt điện thoại; cắm sạc; quota 4G | F-N3/F-N4 là Must thực tế |
| Business/Chi phí | Data 4G tốn tiền; VPS (G3) tốn phí tháng | G1 gần như 0 chi phí hạ tầng; G3 thì không |

**Tổng hợp**: với G1/G2, hệ thống không cần server, chi phí gần 0. Giá trị nằm ở 3 thứ plexo chưa có: **remote route**, **phát hiện trùng upstream** và **lập lịch theo giá/quota**. Rủi ro chính là ép đúng route trên Android/Windows, và Phase 0 phải đo được speedup thật trước khi viết code.

### 10.3 Maturity ladder

| Mức | Bối cảnh | Thách thức |
|-----|----------|-----------|
| Cá nhân (bạn) | 3 thiết bị ở nhà | Ép cellular, quota, pin |
| Nhóm nhỏ / văn phòng | 5–10 điện thoại, nhiều PC dùng chung | Wi-Fi LAN nghẽn; nhiều Coordinator tranh route, cần route reservation |
| Sản phẩm phát hành | Người dùng phổ thông | Ký code Windows (plexo cũng chưa có), driver tether, hỗ trợ đa nhà mạng |

---

## 11. Câu hỏi mở cho user

1. **Goal**: G1 (tải file), G2 (mọi app nhiều kết nối) hay G3 (mọi traffic, cần VPS)?
2. **3 thiết bị cụ thể** là gì? (ví dụ PC Windows + 2 Android? có iPhone không?) Mỗi cái có SIM/ISP nào?
3. **Hệ điều hành máy chính**: Windows, macOS hay Linux? (Linux bind chắc nhất, Windows rủi ro nhất)
4. **Ngôn ngữ quen**: TypeScript (fork plexo) hay Go/Rust?
5. **Quota 4G** mỗi SIM khoảng bao nhiêu GB/tháng, có gói không giới hạn không?

---

## 12. Kết luận + Synthesis Diagram

> "Make it work, make it right, make it fast." — Kent Beck
> Phase 0 (đo với plexo nguyên bản) là bước "make it work", phải làm trước khi viết dòng code nào.

```
┌──────────────── MỐI LIÊN KẾT KHÁI NIỆM — MERGE INTERNET 3 THIẾT BỊ ─────────────────┐
│                                                                                    │
│  GOAL ──► G1 tải file ──► App-level striping (học từ plexo: Range + work-stealing) │
│   │       G2 mọi app  ──► Local Gateway (F-N6): chia theo CONNECTION              │
│   │       G3 1 luồng  ──► VPS bonding (S7, OpenMPTCProuter) — để sau              │
│   ▼                                                                                │
│  RÀNG BUỘC VẬT LÝ: mỗi Route phải có UPSTREAM RIÊNG ──► F-N1 Dedup Detector        │
│   ▼                                                                                │
│  ROUTE abstraction ─┬─ interface (S1 USB / S2 ETH / S3 hotspot) ── bind theo OS    │
│                     ├─ socks     (S4 proxy app trên phone)       ── ép cellular!   │
│                     └─ agent     (S5 app riêng: QR pair, STATUS)  ── mã hóa        │
│   ▼                                                                                │
│  SCHEDULER (work-stealing + racing) ◄── POLICY (F-N3 giá/quota, F-N4 pin/nhiệt)    │
│   ▼                                                                                │
│  Fermi: 100+40+30 Mbps → ~150 Mbps (~1.5×), đổi lại ~41% data đi qua 4G           │
│   ▼                                                                                │
│  ROADMAP: P0 đo (plexo+USB) → P1 fork+SOCKS → P2 Agent Android → P3 G2 → P4 G3     │
│                                                                                    │
└────────────────────────────────────────────────────────────────────────────────────┘
```

---

## Sources & References

### Source 1: Local
| File | Relevance |
|------|-----------|
| (workspace trống, chưa có code) | — |

### Source 3: External
| # | Source | URL | Relevance |
|---|--------|-----|-----------|
| 1 | Plexo README | https://github.com/anmolkapil/plexo | Thuật toán, tính năng, giới hạn |
| 2 | Plexo package.json | https://raw.githubusercontent.com/anmolkapil/plexo/main/package.json | Stack: Electron 44, webtorrent 3.0.21, koffi |
| 3 | Plexo deviceBinding.ts | https://raw.githubusercontent.com/anmolkapil/plexo/main/src/main/network/deviceBinding.ts | `SO_BINDTODEVICE` qua koffi |
| 4 | Speedify vs OpenMPTCProuter (blog vendor, có thiên vị) | https://speedify.com/blog/enterprise-internet-bonding-software/speedify-vs-openmptcprouter-starlink-bonding/ | Phương án S7 / G3 |
| 5 | Speedify — MPTCP & bonding | https://speedify.com/blog/enterprise-internet-bonding-software/what-is-mptcp-and-how-does-internet-bonding-actually-work/ | Cơ chế bonding |
| 6 | TidBITS 2021 [DATED] | https://tidbits.com/2021/08/12/speedify-bonds-multiple-internet-connections-for-speed-and-reliability/ | "Combined bandwidth" ≠ tốc độ 1 luồng; out-of-order |
| 7 | Level1Techs forum | https://forum.level1techs.com/t/alternatives-to-bonding-service-speedify/236339 | Failover OMR vs mwan3 |
| 8 | RFC 8684 — Multipath TCP v1 (paper/spec) | https://www.rfc-editor.org/rfc/rfc8684 | Nền tảng G3 |

**Giới hạn nghiên cứu**: chưa đọc `routes.ts`, `interfaces.ts` hay engine download của plexo; hằng số Windows/macOS ở §2.3 và API Android ở §3.3 cần kiểm chứng với docs chính thức khi code. Freshness check chỉ có 1 search, chưa thấy công cụ mới năm 2026.

---

## Document Lineage

| Version | Document | Focus | Status |
|---------|----------|-------|--------|
| v1 | DESIGN-v1-mergeinternet-multidevice-plexo-inspired.md | Phân tích plexo + ý tưởng 3 thiết bị | Superseded |
| v2 | DESIGN-v2-mergeinternet-g3-bonding-tunnel.md | User chốt goal G3, chuyển sang tunnel bonding qua VPS (Go) | ✅ Current |

## Related Documents

### Suggested Next Documents
- `REPORT-v1-mergeinternet-phase0-baseline.md`: kết quả đo Phase 0
- `SPEC-v1-mergeinternet-agent-protocol.md`: đặc tả chi tiết §5
- `TASK-v1-mergeinternet-build-journey.md`: master tracker

---

## Token Summary
```
External queries: 0 MCP + 5 web fetch + 1 web search
Output:           ~7K tokens
Budget status:    OK (rút gọn có chủ đích theo yêu cầu tiết kiệm quota)
```
