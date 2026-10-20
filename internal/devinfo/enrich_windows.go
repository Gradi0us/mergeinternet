//go:build windows

package devinfo

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	"mergeinternet/internal/bindif"
)

// enrichScript lists, per adapter: connection profile name (= SSID for Wi-Fi),
// PnP id, and for USB adapters the parent composite device's descriptors plus the
// FriendlyName of a WPD device attached to the same parent (phone model / user name).
const enrichScript = `$ErrorActionPreference='SilentlyContinue'
[Console]::OutputEncoding=[Text.Encoding]::UTF8
function Prop($id,$key){ try { (Get-PnpDeviceProperty -InstanceId $id -KeyName $key -ErrorAction Stop).Data } catch { $null } }
$prof=@{}
foreach($p in @(Get-NetConnectionProfile)){ $prof[[int]$p.InterfaceIndex]=[string]$p.Name }
$wpd=@()
foreach($d in @(Get-PnpDevice -Class WPD -PresentOnly)){
  $par=Prop $d.InstanceId 'DEVPKEY_Device_Parent'
  $gpar=$null; if($par){ $gpar=Prop $par 'DEVPKEY_Device_Parent' }
  $wpd+=[pscustomobject]@{Name=[string]$d.FriendlyName;Parent=[string]$par;GParent=[string]$gpar}
}
$res=@()
foreach($a in @(Get-NetAdapter)){
  $pnp=[string]$a.PnPDeviceID
  $parId='';$pd='';$pn='';$w=''
  if($pnp -like 'USB\*'){
    $par=Prop $pnp 'DEVPKEY_Device_Parent'
    if($par){
      $parId=[string]$par
      $pd=[string](Prop $parId 'DEVPKEY_Device_BusReportedDeviceDesc')
      $pn=[string](Prop $parId 'DEVPKEY_Device_FriendlyName')
      $m=$wpd | Where-Object { $_.Parent -ieq $parId -or $_.GParent -ieq $parId } | Select-Object -First 1
      if($m){ $w=[string]$m.Name }
    }
  }
  $idx=[int]$a.InterfaceIndex
  $res+=[pscustomobject]@{Index=$idx;PnPID=$pnp;Profile=[string]$prof[$idx];WPDName=$w;ParentID=$parId;ParentDesc=$pd;ParentName=$pn}
}
ConvertTo-Json -InputObject @($res) -Compress -Depth 3
`

// enrichScan runs the slow PowerShell pass (~4 s).
func enrichScan(ctx context.Context) (map[int]extra, error) {
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	u := utf16.Encode([]rune(enrichScript))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-EncodedCommand", base64.StdEncoding.EncodeToString(b))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("powershell enrichment: %w", err)
	}
	return parseEnrichment(stdout.Bytes())
}

// cheapScan: bindif.List() plus the IPv4 default gateway per adapter (native, ms).
func cheapScan() ([]rawAdapter, error) {
	list, err := bindif.List()
	if err != nil {
		return nil, err
	}
	gws := gateways()
	out := make([]rawAdapter, 0, len(list))
	for _, i := range list {
		r := rawAdapter{Name: i.Name, Index: i.Index, Description: i.Description,
			Kind: string(i.Kind), Up: i.Up, Gateway: gws[i.Index]}
		for _, a := range i.IPv4 {
			r.IPv4 = append(r.IPv4, a.String())
		}
		out = append(out, r)
	}
	return out, nil
}

// gateways maps interface index -> first IPv4 gateway via GetAdaptersAddresses.
func gateways() map[int]string {
	out := map[int]string{}
	size := uint32(16 << 10)
	for try := 0; try < 3; try++ {
		buf := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_INET, 0x80 /* GAA_FLAG_INCLUDE_GATEWAYS */, 0, first, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return out
		}
		for a := first; a != nil; a = a.Next {
			for g := a.FirstGatewayAddress; g != nil; g = g.Next {
				if ip := g.Address.IP(); ip != nil && ip.To4() != nil {
					out[int(a.IfIndex)] = ip.String()
					break
				}
			}
		}
		return out
	}
	return out
}
