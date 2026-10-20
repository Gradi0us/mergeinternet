package main

import "golang.org/x/sys/windows"

var (
	procGetConsoleWindow = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")
	procShowWindow       = windows.NewLazySystemDLL("user32.dll").NewProc("ShowWindow")
)

// hideConsole hides our console window (the copy started with Windows runs in
// the background; opening mi-client.exe again brings up the dashboard).
func hideConsole() {
	if hwnd, _, _ := procGetConsoleWindow.Call(); hwnd != 0 {
		procShowWindow.Call(hwnd, 0) // SW_HIDE
	}
}
