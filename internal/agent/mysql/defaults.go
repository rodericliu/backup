package mysql

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rodericliu/backup/internal/api"
)

// DefaultsFile is a temporary option file holding MySQL credentials. It is
// passed to tools with --defaults-extra-file so that the password never
// appears on a command line (PRD E-4).
type DefaultsFile struct {
	Path string
}

// WriteDefaultsFile writes cfg to a new 0600 file in dir. dir must be visible
// at the same path inside the container. The caller must call Remove.
func WriteDefaultsFile(dir string, cfg api.MySQLConfig) (*DefaultsFile, error) {
	body, err := defaultsContent(cfg)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create credentials dir: %w", err)
	}
	var suffix [8]byte
	_, _ = rand.Read(suffix[:])
	path := filepath.Join(dir, "my-"+hex.EncodeToString(suffix[:])+".cnf")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create credentials file: %w", err)
	}
	_, werr := f.WriteString(body)
	if err := errors.Join(werr, f.Close()); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("write credentials file: %w", err)
	}
	return &DefaultsFile{Path: path}, nil
}

// Remove deletes the file. It is safe to call more than once.
func (d *DefaultsFile) Remove() error {
	if err := os.Remove(d.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove credentials file: %w", err)
	}
	return nil
}

// Arg returns the --defaults-extra-file option. It must be the first option
// passed to mysql or xtrabackup.
func (d *DefaultsFile) Arg() string { return "--defaults-extra-file=" + d.Path }

func defaultsContent(cfg api.MySQLConfig) (string, error) {
	if cfg.User == "" {
		return "", errors.New("mysql user is required")
	}
	var opts []string
	for _, kv := range []struct{ k, v string }{
		{"user", cfg.User},
		{"password", cfg.Password},
		{"socket", cfg.Socket},
		{"host", cfg.Host},
	} {
		if kv.v == "" && kv.k != "password" {
			continue
		}
		v, err := quoteOption(kv.v)
		if err != nil {
			return "", fmt.Errorf("mysql %s: %w", kv.k, err)
		}
		opts = append(opts, kv.k+"="+v)
	}
	if cfg.Port != 0 {
		opts = append(opts, "port="+strconv.Itoa(cfg.Port))
	}
	body := strings.Join(opts, "\n") + "\n"
	// xtrabackup reads [xtrabackup] in addition to [client].
	return "[client]\n" + body + "[xtrabackup]\n" + body, nil
}

// quoteOption quotes an option file value. Option files support backslash
// escapes inside quotes but no way to escape the quote character itself, so
// the quote style is chosen to avoid it.
func quoteOption(v string) (string, error) {
	if strings.ContainsAny(v, "\n\r\x00") {
		return "", errors.New("value must not contain line breaks or NUL")
	}
	v = strings.ReplaceAll(v, `\`, `\\`)
	switch {
	case !strings.Contains(v, `"`):
		return `"` + v + `"`, nil
	case !strings.Contains(v, `'`):
		return `'` + v + `'`, nil
	default:
		return "", errors.New("value must not contain both single and double quotes")
	}
}
