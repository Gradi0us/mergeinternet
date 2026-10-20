// mi-client bonds several internet connections on this machine into one
// tunnel to mi-server. All IPv4 traffic of the machine goes through it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"mergeinternet/internal/bindif"
	"mergeinternet/internal/client"
	"mergeinternet/internal/config"
	"mergeinternet/internal/ui"
)

const usage = `usage:
  mi-client                             open the dashboard (same as "ui")
  mi-client ui [-config client.json]    open the dashboard in the browser
  mi-client run [-config client.json]   start the bonded tunnel without UI (Administrator)
  mi-client interfaces                  list network interfaces and their IPv4
  mi-client genkey                      print a new random key`

func main() {
	log.SetFlags(log.Ltime)
	cmd := "ui"
	args := []string{}
	if len(os.Args) >= 2 {
		cmd, args = os.Args[1], os.Args[2:]
	}
	var err error
	switch cmd {
	case "ui":
		fs := flag.NewFlagSet("ui", flag.ExitOnError)
		cfgPath := fs.String("config", defaultConfigPath(), "config file")
		addr := fs.String("addr", "127.0.0.1:7878", "dashboard listen address (loopback only)")
		noBrowser := fs.Bool("no-browser", false, "do not open the browser")
		hidden := fs.Bool("hidden", false, "hide the console window (start with Windows)")
		autoStart := fs.Bool("autostart", false, "connect right away")
		takeover := fs.Bool("takeover", false, "wait for the previous copy to exit (elevation relaunch)")
		fs.Parse(args)
		if *hidden {
			hideConsole()
		}
		err = runUI(ui.Options{ConfigPath: *cfgPath, Addr: *addr, AutoStart: *autoStart, Takeover: *takeover, Guard: true}, !*noBrowser)
		if errors.Is(err, ui.ErrAlreadyRunning) {
			fmt.Println(err)
			return
		}
	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		cfgPath := fs.String("config", "client.json", "config file")
		fs.Parse(args)
		err = run(*cfgPath)
	case "guard": // started by the dashboard; restores the system proxy if it is killed
		fs := flag.NewFlagSet("guard", flag.ExitOnError)
		pid := fs.Int("pid", 0, "dashboard process to watch")
		backup := fs.String("backup", "", "system proxy backup file")
		fs.Parse(args)
		err = ui.RunGuard(*pid, *backup)
	case "interfaces":
		err = listInterfaces()
	case "genkey":
		fmt.Println(config.GenerateKey())
	default:
		fmt.Println(usage)
		os.Exit(2)
	}
	if err != nil {
		log.Print(err)
		if cmd == "ui" {
			fmt.Print("Nhấn Enter để đóng...")
			fmt.Scanln()
		}
		os.Exit(1)
	}
}

// defaultConfigPath keeps client.json next to the exe, so double-clicking works
// regardless of the working directory.
func defaultConfigPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "client.json"
	}
	return filepath.Join(filepath.Dir(exe), "client.json")
}

func runUI(opts ui.Options, openBrowser bool) error {
	ring := ui.NewLogRing(500)
	log.SetOutput(io.MultiWriter(os.Stderr, ring))
	// Closing the console window arrives as SIGTERM: the connection is stopped
	// and the system proxy restored before exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Println("mergeinternet: bảng điều khiển đang chạy. Đóng cửa sổ này (hoặc Ctrl+C) để thoát và tắt kết nối.")
	opts.Logs = ring
	if openBrowser {
		opts.OpenBrowser = ui.OpenBrowser
	}
	return ui.Run(ctx, opts)
}

func listInterfaces() error {
	ifs, err := bindif.List()
	if err != nil {
		return err
	}
	fmt.Printf("%-5s %-4s %-9s %-34s %s\n", "INDEX", "UP", "KIND", "NAME (use this in client.json)", "IPv4")
	for _, i := range ifs {
		up := "no"
		if i.Up {
			up = "yes"
		}
		fmt.Printf("%-5d %-4s %-9s %-34q %v  %s\n", i.Index, up, i.Kind, i.Name, i.IPv4, i.Description)
	}
	return nil
}

func run(cfgPath string) error {
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.Connection != config.ConnVPS {
		return errors.New("`run` is for VPS mode; use the dashboard (mi-client.exe) for the no-VPS mode")
	}
	t, err := client.Start(cfg, client.LoadUsage(client.UsagePathFor(cfgPath)))
	if err != nil {
		return err
	}
	go t.Engine().LogStats(ctx, 5*time.Second)

	select {
	case <-ctx.Done():
		log.Printf("shutting down, removing routes")
		t.Stop()
		return nil
	case <-t.Done():
		return t.Err()
	}
}
