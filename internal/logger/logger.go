// Package logger configures dockbrr's global leveled logger: a human-readable
// console writer plus an optional rotating file. Level is swappable at runtime
// so a UI change takes effect without a restart.
package logger

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

// logDir is the directory holding log files, or "" when file logging is off.
// Set once by Init; read by Files/Open.
var logDir string

type Config struct {
	Path       string // "" => console only
	Level      string
	MaxSizeMB  int
	MaxBackups int
}

type FileInfo struct {
	Name     string    `json:"name"`
	Modified time.Time `json:"modified"`
	Size     int64     `json:"size"`
}

// ParseLevel normalises and validates a level string.
func ParseLevel(s string) (zerolog.Level, error) {
	return zerolog.ParseLevel(strings.ToLower(strings.TrimSpace(s)))
}

// Init wires the global zerolog logger (console + optional rotating file) and
// redirects the stdlib log package into it. Returns the resolved log directory.
func Init(cfg Config) (string, error) {
	lvl, err := ParseLevel(cfg.Level)
	if err != nil {
		return "", fmt.Errorf("log level %q: %w", cfg.Level, err)
	}
	zerolog.SetGlobalLevel(lvl)
	zerolog.TimeFieldFormat = time.RFC3339

	writers := []io.Writer{zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}}
	logDir = ""
	if cfg.Path != "" {
		dir := filepath.Dir(cfg.Path)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("create log dir: %w", err)
		}
		lj := &lumberjack.Logger{
			Filename:   cfg.Path,
			MaxSize:    cfg.MaxSizeMB,
			MaxBackups: cfg.MaxBackups,
		}
		writers = append(writers, zerolog.ConsoleWriter{Out: lj, TimeFormat: time.RFC3339, NoColor: true})
		logDir = dir
	}
	l := zerolog.New(zerolog.MultiLevelWriter(writers...)).With().Timestamp().Logger()
	zlog.Logger = l
	// Route stray stdlib log.Print (e.g. from third-party deps) into zerolog.
	log.SetFlags(0)
	log.SetOutput(l)
	return logDir, nil
}

// SetLevel swaps the global level atomically.
func SetLevel(level string) error {
	lvl, err := ParseLevel(level)
	if err != nil {
		return err
	}
	zerolog.SetGlobalLevel(lvl)
	return nil
}

func Tracef(f string, a ...any) { zlog.Trace().Msgf(f, a...) }
func Debugf(f string, a ...any) { zlog.Debug().Msgf(f, a...) }
func Infof(f string, a ...any)  { zlog.Info().Msgf(f, a...) }
func Warnf(f string, a ...any)  { zlog.Warn().Msgf(f, a...) }
func Errorf(f string, a ...any) { zlog.Error().Msgf(f, a...) }

// Files lists the log directory, newest first. Empty when file logging is off
// or the directory does not exist yet.
func Files() ([]FileInfo, error) {
	if logDir == "" {
		return []FileInfo{}, nil
	}
	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []FileInfo{}, nil
		}
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, FileInfo{Name: e.Name(), Modified: info.ModTime(), Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// Open opens a named log file for download. name must be a base name that
// resolves inside the log dir. This is the path-traversal guard.
func Open(name string) (io.ReadCloser, error) {
	if logDir == "" {
		return nil, errors.New("file logging disabled")
	}
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return nil, errors.New("invalid log file name")
	}
	full := filepath.Join(logDir, name)
	rel, err := filepath.Rel(logDir, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("invalid log file path")
	}
	return os.Open(full)
}
