// Command backup-server is the central web platform of the MySQL replica
// backup system.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rodericliu/backup/internal/crypto"
	"github.com/rodericliu/backup/internal/logging"
	"github.com/rodericliu/backup/internal/server"
	"github.com/rodericliu/backup/internal/server/config"
	"github.com/rodericliu/backup/internal/store"
	"github.com/rodericliu/backup/internal/version"
)

const usage = `usage: backup-server <command> [flags]

commands:
  run           run the server
  check         validate the configuration
  cert init     create a self-signed CA
  cert issue    issue a server certificate signed by the CA
  keygen        create the master key that encrypts MySQL credentials
  version       print the version
`

// Default locations (PRD 10.1).
const (
	defaultCADir     = "/etc/backup-server/ca"
	defaultTLSDir    = "/etc/backup-server/tls"
	defaultMasterKey = "/etc/backup-server/master.key"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "backup-server:", err)
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
		return runServer(args)
	case "check":
		return check(args, stdout)
	case "cert":
		return cert(args, stdout)
	case "keygen":
		return keygen(args, stdout)
	case "version":
		_, err := fmt.Fprintln(stdout, "backup-server", version.Version)
		return err
	case "help", "-h", "--help":
		_, err := fmt.Fprint(stdout, usage)
		return err
	default:
		return fmt.Errorf("unknown command %q\n%s", cmd, usage)
	}
}

// stringsFlag collects a repeatable flag.
type stringsFlag []string

func (s *stringsFlag) String() string { return strings.Join(*s, ",") }

func (s *stringsFlag) Set(v string) error {
	for h := range strings.SplitSeq(v, ",") {
		if h = strings.TrimSpace(h); h != "" {
			*s = append(*s, h)
		}
	}
	return nil
}

func cert(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: backup-server cert init|issue [flags]")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "init":
		fs := flag.NewFlagSet("cert init", flag.ContinueOnError)
		dir := fs.String("dir", defaultCADir, "directory for ca.crt and ca.key")
		cn := fs.String("cn", "backup-server CA", "CA common name")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if err := crypto.InitCA(*dir, *cn, time.Now()); err != nil {
			return err
		}
		_, err := fmt.Fprintf(stdout, "created %s and %s\ncopy %[1]s to every agent as server_ca; keep %[2]s secret\n",
			filepath.Join(*dir, crypto.CACertFile), filepath.Join(*dir, crypto.CAKeyFile))
		return err
	case "issue":
		fs := flag.NewFlagSet("cert issue", flag.ContinueOnError)
		caDir := fs.String("ca-dir", defaultCADir, "directory of the CA")
		out := fs.String("out", defaultTLSDir, "directory for server.crt and server.key")
		var hosts stringsFlag
		fs.Var(&hosts, "host", "DNS name or IP address of the server (repeatable or comma separated)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if len(hosts) == 0 {
			return errors.New("cert issue: at least one -host is required")
		}
		if err := crypto.IssueServerCert(*caDir, *out, hosts, time.Now()); err != nil {
			return err
		}
		_, err := fmt.Fprintf(stdout, "issued %s for %s\n", filepath.Join(*out, crypto.ServerCertFile), strings.Join(hosts, ", "))
		return err
	default:
		return fmt.Errorf("unknown cert command %q", sub)
	}
}

func keygen(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	out := fs.String("out", defaultMasterKey, "master key file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := crypto.GenerateKeyFile(*out); err != nil {
		return err
	}
	_, err := fmt.Fprintf(stdout, "created %s; back it up: encrypted MySQL passwords cannot be recovered without it\n", *out)
	return err
}

func configFlag(name string, args []string) (string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	path := fs.String("config", config.DefaultPath, "configuration file")
	err := fs.Parse(args)
	return *path, err
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
	if _, err := server.LoadTLS(cfg.TLS.Cert, cfg.TLS.Key); err != nil {
		return err
	}
	if _, err := crypto.LoadKeyFile(cfg.Secrets.MasterKeyFile); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "configuration %s is valid\n", path)
	return err
}

func runServer(args []string) error {
	path, err := configFlag("run", args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	log, closer, err := logging.New(cfg.Log.Level, cfg.Log.Dir, "backup-server")
	if err != nil {
		return err
	}
	defer closer.Close()
	if err := serve(cfg, log); err != nil {
		log.Error("backup-server failed", "err", err)
		return err
	}
	log.Info("backup-server stopped")
	return nil
}

func serve(cfg *config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	tlsCfg, err := server.LoadTLS(cfg.TLS.Cert, cfg.TLS.Key)
	if err != nil {
		return err
	}
	key, err := crypto.LoadKeyFile(cfg.Secrets.MasterKeyFile)
	if err != nil {
		return err
	}
	sealer, err := crypto.NewSealer(key)
	if err != nil {
		return err
	}
	if err := cfg.Assistant.ReadAPIKey(); err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.Database.DSN(), cfg.Database.MaxConns)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		return err
	}
	srv := server.New(server.Options{Store: st, Sealer: sealer, Config: cfg, Logger: log})
	if err := srv.Bootstrap(ctx); err != nil {
		return err
	}
	log.Info("backup-server started", "version", version.Version, "listen", cfg.Listen, "agent_listen", cfg.AgentListen)
	return srv.Run(ctx, tlsCfg)
}
