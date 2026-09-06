package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/buildinfo"
	"github.com/lum1t4/wpx/internal/config"
	"github.com/lum1t4/wpx/internal/dns"
	"github.com/lum1t4/wpx/internal/install"
	"github.com/lum1t4/wpx/internal/panelaccess"
	"github.com/lum1t4/wpx/internal/provision"
	"github.com/lum1t4/wpx/internal/store"
	"github.com/lum1t4/wpx/internal/updatecheck"
	"github.com/lum1t4/wpx/internal/upgrade"
	"github.com/lum1t4/wpx/internal/web"
	"github.com/lum1t4/wpx/internal/worker"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "wpx:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "version":
		fmt.Printf("wpx %s (%s, %s)\n", buildinfo.Version, buildinfo.Commit, buildinfo.Date)
		return nil
	case "serve":
		return runServe(args[1:])
	case "broker":
		return runBroker(args[1:])
	case "install":
		return runInstall(args[1:])
	case "access":
		return runAccess(args[1:])
	case "upgrade":
		return runUpgrade(args[1:])
	case "updates":
		return runUpdates(args[1:])
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Println(`WPX server panel

Usage:
  wpx version
  wpx install [--dry-run] [--no-update-checks]
  wpx access (--local | --public | --tailscale | --domain panel.example.com)
  wpx upgrade [--config /etc/wpx/config.json]
  wpx updates (--enable | --disable | --status) [--config /etc/wpx/config.json]
  wpx serve --config /etc/wpx/config.json
  wpx broker --config /etc/wpx/config.json`)
}

func runUpdates(args []string) error {
	set := flag.NewFlagSet("updates", flag.ContinueOnError)
	enable := set.Bool("enable", false, "enable the daily release check")
	disable := set.Bool("disable", false, "disable the daily release check")
	status := set.Bool("status", false, "show whether release checks are enabled")
	configPath := set.String("config", "/etc/wpx/config.json", "configuration path")
	if err := set.Parse(args); err != nil {
		return err
	}
	selected := 0
	for _, choice := range []bool{*enable, *disable, *status} {
		if choice {
			selected++
		}
	}
	if selected != 1 {
		return errors.New("choose exactly one of --enable, --disable, or --status")
	}
	cleanPath := filepath.Clean(*configPath)
	cfg, err := config.Load(cleanPath)
	if err != nil {
		return err
	}
	if *status {
		if cfg.UpdateChecks {
			fmt.Printf("Automatic release checks are enabled. Endpoint: %s\n", cfg.UpdateCheckURL)
		} else {
			fmt.Println("Automatic release checks are disabled.")
		}
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("changing update checks must run as root")
	}
	previous := cfg
	cfg.UpdateChecks = *enable
	if err := config.Save(cleanPath, cfg); err != nil {
		return err
	}
	if err := exec.Command("/usr/bin/systemctl", "restart", "wpx.service").Run(); err != nil {
		_ = config.Save(cleanPath, previous)
		_ = exec.Command("/usr/bin/systemctl", "restart", "wpx.service").Run()
		return fmt.Errorf("restart WPX after changing update checks: %w", err)
	}
	state := "disabled"
	if cfg.UpdateChecks {
		state = "enabled"
	}
	fmt.Printf("Automatic release checks are %s.\n", state)
	return nil
}

func runUpgrade(args []string) error {
	set := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	configPath := set.String("config", "/etc/wpx/config.json", "configuration path")
	if err := set.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := upgrade.Run(ctx, upgrade.Options{ConfigPath: filepath.Clean(*configPath)})
	if err != nil {
		return err
	}
	fmt.Printf("WPX upgraded successfully. Recovery snapshot: %s\n", result.BackupDirectory)
	return nil
}

func runAccess(args []string) error {
	set := flag.NewFlagSet("access", flag.ContinueOnError)
	local := set.Bool("local", false, "listen on loopback only")
	public := set.Bool("public", false, "listen on every interface")
	tailscale := set.Bool("tailscale", false, "publish through Tailscale Serve")
	domain := set.String("domain", "", "publish through Nginx with a trusted certificate")
	configPath := set.String("config", "/etc/wpx/config.json", "configuration path")
	if err := set.Parse(args); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("access configuration must run as root")
	}
	mode, selected := "", 0
	for name, enabled := range map[string]bool{"local": *local, "public": *public, "tailscale": *tailscale, "domain": strings.TrimSpace(*domain) != ""} {
		if enabled {
			mode, selected = name, selected+1
		}
	}
	if selected != 1 {
		return errors.New("choose exactly one of --local, --public, --tailscale, or --domain")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := panelaccess.Configure(ctx, panelaccess.Options{ConfigPath: filepath.Clean(*configPath), Mode: mode, Domain: *domain})
	if err != nil {
		return err
	}
	fmt.Printf("Panel access updated: %s\n", result.URL)
	return nil
}

func runServe(args []string) error {
	set := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := set.String("config", "/etc/wpx/config.json", "configuration path")
	if err := set.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	state, err := store.Open(cfg.StatePath)
	if err != nil {
		return err
	}
	defer state.Close()
	secretKey, err := os.ReadFile(cfg.SecretKeyPath)
	if err != nil {
		return fmt.Errorf("read application secret key: %w", err)
	}
	if err := state.ConfigureSecretKey(secretKey); err != nil {
		return err
	}
	webBrokerClient := broker.Client{SocketPath: cfg.BrokerSocket, Timeout: 15 * time.Second}
	server, err := web.New(cfg, state, webBrokerClient, slog.Default())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	jobBrokerClient := broker.Client{SocketPath: cfg.BrokerSocket, Timeout: 30 * time.Minute}
	jobWorker := &worker.Worker{Store: state, Provisioner: worker.BrokerProvisioner{Client: jobBrokerClient}, DNS: dns.NewClient(), Logger: slog.Default()}
	go func() {
		if err := jobWorker.Run(ctx); err != nil {
			slog.Error("durable worker stopped", "error", err)
			stop()
		}
	}()
	if cfg.UpdateChecks {
		go runUpdateChecks(ctx, state, cfg.UpdateCheckURL)
	}
	slog.Info("starting WPX web control plane", "address", cfg.ListenAddress)
	return server.ListenAndServe(ctx)
}

func runUpdateChecks(ctx context.Context, state *store.Store, endpoint string) {
	checker := updatecheck.Client{}
	check := func() {
		due, err := state.UpdateCheckDue(ctx, 24*time.Hour)
		if err != nil || !due {
			return
		}
		previous, _ := state.UpdateStatus(ctx)
		checkContext, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		result, checkErr := checker.Check(checkContext, endpoint)
		if checkErr != nil {
			if err := state.RecordUpdateCheck(ctx, buildinfo.Version, previous.LatestVersion, previous.ReleaseURL, checkErr); err != nil {
				slog.Error("record failed update check", "error", err)
			}
			return
		}
		if err := state.RecordUpdateCheck(ctx, buildinfo.Version, result.Version, result.URL, nil); err != nil {
			slog.Error("record update check", "error", err)
		}
	}
	check()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

func runBroker(args []string) error {
	set := flag.NewFlagSet("broker", flag.ContinueOnError)
	configPath := set.String("config", "/etc/wpx/config.json", "configuration path")
	if err := set.Parse(args); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("broker mode must run as root")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	wpxGroup, err := lookupGroupID("wpx")
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	host := provision.DefaultHost(cfg.SiteRoot, cfg.DataRoot)
	server := &broker.Server{
		SocketPath:  cfg.BrokerSocket,
		SiteRoot:    cfg.SiteRoot,
		AllowedUID:  cfg.WebUID,
		SocketGID:   wpxGroup,
		Provisioner: host,
		WordPress:   host,
		Files:       host,
		Backups:     host,
		Metrics:     host,
		Databases:   host,
	}
	slog.Info("starting WPX privileged broker", "socket", cfg.BrokerSocket)
	return server.Run(ctx)
}

func lookupGroupID(name string) (int, error) {
	b, err := os.ReadFile("/etc/group")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) >= 3 && parts[0] == name {
			if gid, err := strconv.Atoi(parts[2]); err == nil {
				return gid, nil
			}
		}
	}
	return 0, fmt.Errorf("group %q not found", name)
}

func runInstall(args []string) error {
	set := flag.NewFlagSet("install", flag.ContinueOnError)
	dryRun := set.Bool("dry-run", false, "show installation plan without changing the host")
	noUpdateChecks := set.Bool("no-update-checks", false, "disable the daily GitHub release check")
	configPath := set.String("config", "/etc/wpx/config.json", "configuration path")
	if err := set.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := install.Run(ctx, install.Options{DryRun: *dryRun, DisableUpdateChecks: *noUpdateChecks, ConfigPath: filepath.Clean(*configPath), Output: os.Stdout})
	if err != nil {
		return err
	}
	if !*dryRun {
		fmt.Printf("\nWPX is ready.\nPanel: %s\nOne-time bootstrap token: %s\n", result.PanelURL, result.BootstrapToken)
	}
	return nil
}
