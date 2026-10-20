// Package logx is ego's leveled logger: the standard library's log/slog with printf-style helpers, so a message
// keeps its wording and gains a level that --log-level can filter on.
package logx

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// ParseLevel reads debug, info, warn or error (any case). Empty means info.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", s)
}

// Setup makes level the threshold of the process-wide logger, writing text lines to stderr.
func Setup(level string) error {
	return setup(level, os.Stderr)
}

func setup(level string, w io.Writer) error {
	l, err := ParseLevel(level)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: l})))
	return nil
}

func Debugf(format string, args ...any) { slog.Debug(fmt.Sprintf(format, args...)) }
func Infof(format string, args ...any)  { slog.Info(fmt.Sprintf(format, args...)) }
func Warnf(format string, args ...any)  { slog.Warn(fmt.Sprintf(format, args...)) }
func Errorf(format string, args ...any) { slog.Error(fmt.Sprintf(format, args...)) }
