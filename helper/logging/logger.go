package logging

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	log "github.com/hashicorp/go-hclog"
)

const (
	UnspecifiedFormat LogFormat = iota
	StandardFormat
	JSONFormat
)

// defaultRotateDuration is the default time taken by the agent to rotate logs
const defaultRotateDuration = 24 * time.Hour

type LogFormat int

// LogConfig should be used to supply configuration when creating a new Vault logger
type LogConfig struct {
	// Name is the name the returned logger will use to prefix log lines.
	Name string

	// LogLevel is the minimum level to be logged.
	LogLevel log.Level

	// LogFormat is the log format to use, supported formats are 'standard' and 'json'.
	LogFormat LogFormat

	// LogFilePath is the path to write the logs to the user specified file.
	LogFilePath string

	// LogRotateDuration is the user specified time to rotate logs
	LogRotateDuration time.Duration

	// LogRotateBytes is the user specified byte limit to rotate logs
	LogRotateBytes int

	// LogRotateMaxFiles is the maximum number of past archived log files to keep
	LogRotateMaxFiles int
}

func (c LogConfig) IsFormatJson() bool {
	return c.LogFormat == JSONFormat
}

// Stringer implementation
func (lf LogFormat) String() string {
	switch lf {
	case UnspecifiedFormat:
		return "unspecified"
	case StandardFormat:
		return "standard"
	case JSONFormat:
		return "json"
	}

	// unreachable
	return "unknown"
}

// noErrorWriter is a wrapper to suppress errors when writing to w.
type noErrorWriter struct {
	w io.Writer
}

func (w noErrorWriter) Write(p []byte) (n int, err error) {
	_, _ = w.w.Write(p)
	// We purposely return n == len(p) as if write was successful
	return len(p), nil
}

// Setup creates a new logger with the specified configuration and writer
func Setup(config *LogConfig, w io.Writer) (log.InterceptLogger, error) {
	// Validate the log level
	if config.LogLevel.String() == "unknown" {
		return nil, fmt.Errorf("invalid log level: %v", config.LogLevel)
	}

	// If out is os.Stdout and Vault is being run as a Windows Service, writes will
	// fail silently, which may inadvertently prevent writes to other writers.
	// noErrorWriter is used as a wrapper to suppress any errors when writing to out.
	writers := []io.Writer{noErrorWriter{w: w}}

	// Create a file logger if the user has specified the path to the log file
	if config.LogFilePath != "" {
		dir, fileName := filepath.Split(config.LogFilePath)
		if fileName == "" {
			fileName = "vault.log"
		}
		if config.LogRotateDuration == 0 {
			config.LogRotateDuration = defaultRotateDuration
		}
		logFile := &LogFile{
			fileName: fileName,
			logPath:  dir,
			duration: config.LogRotateDuration,
			maxBytes: config.LogRotateBytes,
			maxFiles: config.LogRotateMaxFiles,
		}
		if err := logFile.pruneFiles(); err != nil {
			return nil, fmt.Errorf("failed to prune log files: %w", err)
		}
		if err := logFile.openNew(); err != nil {
			return nil, fmt.Errorf("failed to setup logging: %w", err)
		}
		writers = append(writers, logFile)
	}

	logger := log.NewInterceptLogger(&log.LoggerOptions{
		Name:       config.Name,
		Level:      config.LogLevel,
		Output:     io.MultiWriter(writers...),
		JSONFormat: config.IsFormatJson(),
	})
	return logger, nil
}

// ParseLogFormat parses the log format from the provided string.
func ParseLogFormat(format string) (LogFormat, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "":
		return UnspecifiedFormat, nil
	case "standard":
		return StandardFormat, nil
	case "json":
		return JSONFormat, nil
	default:
		return UnspecifiedFormat, fmt.Errorf("unknown log format: %s", format)
	}
}

func ParseLogLevel(logLevel string) (log.Level, error) {
	var result log.Level
	logLevel = strings.ToLower(strings.TrimSpace(logLevel))

	switch logLevel {
	case "trace":
		result = log.Trace
	case "debug":
		result = log.Debug
	case "notice", "info", "":
		result = log.Info
	case "warn", "warning":
		result = log.Warn
	case "err", "error":
		result = log.Error
	default:
		return -1, errors.New(fmt.Sprintf("unknown log level: %s", logLevel))
	}

	return result, nil
}
