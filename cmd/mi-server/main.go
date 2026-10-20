// mi-server runs on a Linux VPS: it receives the client's packets over all
// paths, restores their order, forwards them to the internet (via NAT set up
// by scripts/server-setup.sh) and spreads the replies back over the paths.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mergeinternet/internal/config"
	"mergeinternet/internal/engine"
	"mergeinternet/internal/tunif"
)

const usage = `usage:
  mi-server run [-config server.json]   start the server (root)
  mi-server genkey                      print a new random key`

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	if len(os.Args) < 2 {
		fmt.Println(usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		cfgPath := fs.String("config", "server.json", "config file")
		fs.Parse(os.Args[2:])
		if err := run(*cfgPath); err != nil {
			log.Fatal(err)
		}
	case "genkey":
		fmt.Println(config.GenerateKey())
	default:
		fmt.Println(usage)
		os.Exit(2)
	}
}

func run(cfgPath string) error {
	cfg, err := config.LoadServer(cfgPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	laddr, err := net.ResolveUDPAddr("udp4", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	conn, err := net.ListenUDP("udp4", laddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	engine.SetSocketBuffers(conn)

	dev, err := tunif.Open(cfg.Tun.Name, cfg.Tun.MTU)
	if err != nil {
		return err
	}
	defer dev.Close()
	if err := tunif.ConfigureServer(dev.Name(), cfg.TunPrefix, cfg.Tun.MTU); err != nil {
		return err
	}

	eng, err := engine.NewServer(engine.Config{
		Key:          cfg.KeyBytes,
		Mode:         cfg.SchedMode,
		ReorderDelay: cfg.Reorder,
		Deliver:      dev.Write,
		Logf:         log.Printf,
	}, func(b []byte, to netip.AddrPort) error {
		_, err := conn.WriteToUDPAddrPort(b, to)
		return err
	})
	if err != nil {
		return err
	}
	log.Printf("mi-server: listening on %s, mode=%s, reorder=%s, tun=%s %s",
		conn.LocalAddr(), cfg.SchedMode, cfg.Reorder, dev.Name(), cfg.TunPrefix)

	go eng.Run(ctx)
	go eng.LogStats(ctx, 10*time.Second)

	errc := make(chan error, 2)
	go func() {
		buf := make([]byte, 65535)
		for {
			n, from, err := conn.ReadFromUDPAddrPort(buf)
			if err != nil {
				errc <- fmt.Errorf("udp read: %w", err)
				return
			}
			eng.Inbound(buf[:n], from)
		}
	}()
	go func() {
		for {
			pkt, err := dev.Read()
			if err != nil {
				errc <- fmt.Errorf("tun read: %w", err)
				return
			}
			if len(pkt) > 0 && pkt[0]>>4 == 4 {
				eng.SendPacket(pkt)
			}
		}
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		return err
	}
}
