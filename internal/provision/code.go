package provision

import (
	"encoding/base64"
	"errors"
	"net/netip"
	"strconv"
	"strings"
)

const keyLen = 32

// validKey reports whether s is a base64 encoded 32-byte key.
func validKey(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(b) == keyLen
}

// ParseConnectionCode parses "mi1:<ipv4>:<port>:<key>" (whitespace-trimmed)
// into the server address ("ipv4:port") and the key, validating both.
func ParseConnectionCode(code string) (serverAddr, key string, err error) {
	parts := strings.Split(strings.TrimSpace(code), ":")
	if len(parts) != 4 || parts[0] != "mi1" {
		return "", "", errors.New("Mã kết nối không đúng định dạng (cần dạng mi1:IP:cổng:key).")
	}
	ip, perr := netip.ParseAddr(strings.TrimSpace(parts[1]))
	if perr != nil || !ip.Is4() {
		return "", "", errors.New("Mã kết nối có địa chỉ IP không hợp lệ.")
	}
	port, perr := strconv.Atoi(strings.TrimSpace(parts[2]))
	if perr != nil || port < 1 || port > 65535 {
		return "", "", errors.New("Mã kết nối có cổng không hợp lệ.")
	}
	key = strings.TrimSpace(parts[3])
	if !validKey(key) {
		return "", "", errors.New("Mã kết nối có key không hợp lệ (cần 32 byte base64).")
	}
	return netip.AddrPortFrom(ip, uint16(port)).String(), key, nil
}
