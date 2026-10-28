// Command backup-agent runs scheduled xtrabackup backups of the MySQL replica
// containers on one host.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/rodericliu/backup/internal/agent"
	"github.com/rodericliu/backup/internal/agent/config"
	"github.com/rodericliu/backup/internal/logging"
	"github.com/rodericliu/backup/internal/schedule"
	"github.com/rodericliu/backup/internal/version"
)

const usage = `usage: backup-agent <command> [flags]

commands:
  run       run the agent
  check     validate the configuration and print the upcoming schedule
  version   print the version
`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "backup-agent:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing command\n" + usage)
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "run":
		return runAgent(args)
	case "check":
		return check(args, stdout)
	case "version":
		_, err := fmt.Fprintln(stdout, "backup-agent", version.Version)
		return err
	case "help", "-h", "--help":
		_, err := fmt.Fprint(stdout, usage)
		return err
	default:
		return fmt.Errorf("unknown command %q\n%s", cmd, usage)
	}
}

func configFlag(name string, args []string) (string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	path := fs.String("config", config.DefaultPath, "configuration file")
	err := fs.Parse(args)
	return *path, err
}

func runAgent(args []string) error {
	path, err := configFlag("run", args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	log, closer, err := logging.New(cfg.Log.Level, cfg.Log.Dir, "backup-agent")
	if err != nil {
		return err
	}
	defer closer.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a, err := agent.New(ctx, cfg, log)
	if err != nil {
		log.Error("start agent", "err", err)
		return err
	}
	defer func() {
		if err := a.Close(); err != nil {
			log.Error("close agent", "err", err)
		}
	}()

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				next, err := config.Load(path)
				if err == nil {
					err = a.Reload(ctx, next)
				}
				if err != nil {
					log.Error("reload configuration", "err", err)
					continue
				}
				log.Info("configuration reloaded")
			}
		}
	}()

	log.Info("backup-agent started", "version", version.Version, "config", path, "backup_root", cfg.BackupRoot)
	err = a.Run(ctx)
	log.Info("backup-agent stopped")
	return err
}

func check(args []string, stdout io.Writer) error {
	path, err := configFlag("check", args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	now := time.Now().In(cfg.Location)
	slots := schedule.Slots(cfg.Projects, cfg.Location, now, now.Add(24*time.Hour))

	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "configuration %s is valid: %d projects\n\n", path, len(cfg.Projects))
	fmt.Fprintln(w, "PROJECT\tCONTAINER\tTIER\tFULL\tINCR\tOFFSET\tMODE\tRETENTION\tENABLED")
	for _, p := range cfg.Projects {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%dd\t%v\n",
			p.Name, p.Container, p.Tier, p.FullInterval, p.IncrInterval, p.Offset, p.IncrMode, p.RetentionDays, p.Enabled())
	}
	fmt.Fprintf(w, "\nnext 24h (%s):\n", cfg.Location)
	fmt.Fprintln(w, "TIME\tTYPE\tPROJECT")
	for _, s := range slots {
		fmt.Fprintf(w, "%s\t%s\t%s\n", s.At.In(cfg.Location).Format("01-02 15:04"), s.Type, s.Project)
	}
	return w.Flush()
}
