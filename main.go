package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/build"
	"boot.dev/linko/internal/linkoerr"
	"boot.dev/linko/internal/store"
	pkgerr "github.com/pkg/errors"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	status := run(ctx, cancel, *httpPort, *dataDir)
	cancel()
	os.Exit(status)
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string) int {
	accessLogFile, err := os.OpenFile("linko.access.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("failed to open access log file: %v", err)
		return 1
	}
	defer accessLogFile.Close()
	logger, closer := initializeLogger(os.Getenv("LINKO_LOG_FILE"))
	defer closer()
	hostname, _ := os.Hostname()
	logger = logger.With(
		slog.String("git_sha", build.GitSHA),
		slog.String("build_time", build.BuildTime),
		slog.String("env", os.Getenv("ENV")),
		slog.String("hostname", hostname),
	)
	st, err := store.New(dataDir, logger)
	if err != nil {
		logger.Error("failed to create store", "error", err)
		return 1
	}
	s := newServer(*st, httpPort, cancel, logger)
	var serverErr error
	logger.Debug("Linko is running on http://localhost", "port", httpPort)
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	logger.Debug("Linko is shutting down")

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Error("failed to shutdown server", "error", err)
		return 1
	}
	if serverErr != nil {
		logger.Error("server error", "error", serverErr)
		return 1
	}
	return 0
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "error" {
		err, ok := a.Value.Any().(error)
		if !ok {
			return a
		}
		atts := linkoerr.Attrs(err)
		errorAttrs := []slog.Attr{
			slog.String("message", err.Error()),
		}

		if stackErr, ok := errors.AsType[stackTracer](err); ok {
			errorAttrs = append(errorAttrs, slog.Attr{
				Key:   "stack_trace",
				Value: slog.StringValue(fmt.Sprintf("%+v", stackErr.StackTrace())),
			})
		} else if multiErr, ok := errors.AsType[multiError](err); ok {
			for i, individualErr := range multiErr.Unwrap() {
				attrs := linkoerr.Attrs(individualErr)
				attrArgs := make([]any, len(attrs))
				for j, attr := range attrs {
					attrArgs[j] = attr
				}
				errorAttrs = append(
					errorAttrs,
					slog.Group(fmt.Sprintf("error_%d", i+1), attrArgs...),
				)
			}
		} else {
			errorAttrs = append(errorAttrs, atts...)
		}
		return slog.GroupAttrs("error", errorAttrs...)
	}
	return a
}

type closeFunc func() error

type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}
type multiError interface {
	error
	Unwrap() []error
}

func initializeLogger(logFile string) (*slog.Logger, closeFunc) {
	debugHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level:       slog.LevelDebug,
		ReplaceAttr: replaceAttr,
	})
	if logFile != "" {
		file, err := os.OpenFile(logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			slog.Error("failed to open log file", "error", err)
		}
		buffer := bufio.NewWriterSize(file, 8192)
		mulWriter := io.MultiWriter(os.Stderr, buffer)
		infoHandler := slog.NewJSONHandler(mulWriter, &slog.HandlerOptions{
			Level:       slog.LevelInfo,
			ReplaceAttr: replaceAttr,
		})
		handler := slog.NewMultiHandler(debugHandler, infoHandler)
		return slog.New(handler), func() error {
			buffer.Flush()
			file.Close()
			return nil
		}
	} else {
		Writer := os.Stderr
		infoHandler := slog.NewJSONHandler(Writer, &slog.HandlerOptions{
			Level:       slog.LevelInfo,
			ReplaceAttr: replaceAttr,
		})
		handler := slog.NewMultiHandler(debugHandler, infoHandler)
		return slog.New(handler), func() error {
			return nil
		}
	}
}
