package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/store"
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
	st, err := store.New(dataDir, logger)
	if err != nil {
		logger.Error(fmt.Sprintf("failed to create store: %v", err))
		return 1
	}
	s := newServer(*st, httpPort, cancel, logger)
	var serverErr error
	logger.Debug("Linko is running on http://localhost:%d", httpPort)
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	logger.Debug("Linko is shutting down")

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Error(fmt.Sprintf("failed to shutdown server: %v", err))
		return 1
	}
	if serverErr != nil {
		logger.Error(fmt.Sprintf("server error: %v", serverErr))
		return 1
	}
	return 0
}

type closeFunc func() error

func initializeLogger(logFile string) (*slog.Logger, closeFunc) {
	debugHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	if logFile != "" {
		file, err := os.OpenFile(logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			slog.Error(fmt.Sprintf("failed to open log file: %v", err))
		}
		buffer := bufio.NewWriterSize(file, 8192)
		mulWriter := io.MultiWriter(os.Stderr, buffer)
		infoHandler := slog.NewTextHandler(mulWriter, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})
		handler := slog.NewMultiHandler(debugHandler, infoHandler)
		return slog.New(handler), func() error {
			buffer.Flush()
			file.Close()
			return nil
		}
	} else {
		Writer := os.Stderr
		infoHandler := slog.NewTextHandler(Writer, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})
		handler := slog.NewMultiHandler(debugHandler, infoHandler)
		return slog.New(handler), func() error {
			return nil
		}
	}
}
