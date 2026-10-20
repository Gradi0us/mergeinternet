package sysproxy

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	wininet         = windows.NewLazySystemDLL("wininet.dll")
	procSetOption   = wininet.NewProc("InternetSetOptionW")
	procQueryOption = wininet.NewProc("InternetQueryOptionW")
	kernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalFree  = kernel32.NewProc("GlobalFree")
)

// wininet.h
const (
	optRefresh         = 37
	optSettingsChanged = 39
	optPerConnection   = 75

	perConnFlags      = 1
	perConnServer     = 2
	perConnBypass     = 3
	perConnAutoConfig = 4
)

// INTERNET_PER_CONN_OPTIONW: DWORD + union { DWORD; LPWSTR; FILETIME }.
type perConnOption struct {
	option uint32
	value  uint64 // DWORD, pointer or FILETIME; Go adds the same padding as C on amd64
}

// INTERNET_PER_CONN_OPTION_LISTW.
type perConnList struct {
	size       uint32
	connection *uint16 // nil = the LAN / default connection
	count      uint32
	errIndex   uint32
	options    *perConnOption
}

func platformGet() (Settings, error) {
	opts := []perConnOption{{option: perConnFlags}, {option: perConnServer}, {option: perConnBypass}, {option: perConnAutoConfig}}
	list := perConnList{count: uint32(len(opts)), options: &opts[0]}
	list.size = uint32(unsafe.Sizeof(list))
	size := list.size
	r, _, err := procQueryOption.Call(0, optPerConnection, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return Settings{}, fmt.Errorf("đọc cài đặt proxy: %w", err)
	}
	s := Settings{Flags: uint32(opts[0].value)}
	s.ProxyServer = takeString(&opts[1])
	s.Bypass = takeString(&opts[2])
	s.AutoConfigURL = takeString(&opts[3])
	return s, nil
}

// strField views the option's 8-byte union as the LPWSTR member.
func strField(o *perConnOption) **uint16 { return (**uint16)(unsafe.Pointer(&o.value)) }

// takeString reads and frees a string WinINet allocated for a query.
func takeString(o *perConnOption) string {
	p := *strField(o)
	if p == nil {
		return ""
	}
	s := windows.UTF16PtrToString(p)
	procGlobalFree.Call(uintptr(unsafe.Pointer(p)))
	return s
}

func platformSet(s Settings) error {
	server, _ := windows.UTF16PtrFromString(s.ProxyServer)
	bypass, _ := windows.UTF16PtrFromString(s.Bypass)
	auto, _ := windows.UTF16PtrFromString(s.AutoConfigURL)
	opts := []perConnOption{
		{option: perConnFlags, value: uint64(s.Flags)},
		{option: perConnServer}, {option: perConnBypass}, {option: perConnAutoConfig},
	}
	*strField(&opts[1]) = server
	*strField(&opts[2]) = bypass
	*strField(&opts[3]) = auto
	list := perConnList{count: uint32(len(opts)), options: &opts[0]}
	list.size = uint32(unsafe.Sizeof(list))
	r, _, err := procSetOption.Call(0, optPerConnection, uintptr(unsafe.Pointer(&list)), uintptr(list.size))
	// The pointers above live inside uint64 fields the GC cannot see: keep the
	// UTF-16 buffers alive until WinINet has copied them.
	runtime.KeepAlive(server)
	runtime.KeepAlive(bypass)
	runtime.KeepAlive(auto)
	if r == 0 {
		return fmt.Errorf("đặt proxy hệ thống: %w", err)
	}
	// Tell running applications to re-read the settings.
	procSetOption.Call(0, optSettingsChanged, 0, 0)
	procSetOption.Call(0, optRefresh, 0, 0)
	return nil
}
