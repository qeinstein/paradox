package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	paradoxserver "github.com/qeinstein/paradox/server"
	"github.com/qeinstein/paradox/version"
)

func serveCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:8080", "HTTP listen address")
	runsDir := fs.String("runs-dir", defaultRunsDir(), "directory containing run artifacts")
	evalDir := fs.String("eval-dir", defaultEvalDir(), "directory containing evaluation reports")
	apiKey := fs.String("api-key", envFirst("PARADOX_API_KEY"), "API key for non-loopback bindings (prefer PARADOX_API_KEY)")
	readHeaderTimeout := fs.Duration("read-header-timeout", 5*time.Second, "HTTP read header timeout")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "serve: unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		fmt.Fprintf(stderr, "serve: invalid address %q: %v\n", *addr, err)
		return 2
	}
	key := strings.TrimSpace(*apiKey)
	if !isLoopbackHost(host) && key == "" {
		fmt.Fprintf(stderr, "serve: refusing to bind non-loopback address %q without an API key\n", *addr)
		fmt.Fprintln(stderr, "serve: set PARADOX_API_KEY or use --api-key, or bind 127.0.0.1")
		return 2
	}
	server, err := paradoxserver.New(paradoxserver.Config{
		RunsDir: *runsDir, EvalDir: *evalDir, APIKey: key, Version: version.Current().Version,
	})
	if err != nil {
		fmt.Fprintf(stderr, "serve: %v\n", err)
		return 1
	}
	httpServer := &http.Server{
		Addr: *addr, Handler: server.Handler(),
		ReadHeaderTimeout: *readHeaderTimeout,
	}
	listenErr := make(chan error, 1)
	go func() {
		listenErr <- httpServer.ListenAndServe()
	}()
	fmt.Fprintf(stdout, "PARADOX %s listening on http://%s\n", version.Current().String(), *addr)
	if key == "" {
		fmt.Fprintln(stdout, "  auth: disabled (loopback binding only)")
	} else {
		fmt.Fprintln(stdout, "  auth: X-API-Key or Authorization: Bearer required for /api/*")
	}
	fmt.Fprintf(stdout, "  runs: %s\n  evals: %s\n", *runsDir, *evalDir)

	select {
	case <-ctx.Done():
		fmt.Fprintln(stdout, "shutting down...")
	case err := <-listenErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(stderr, "serve: %v\n", err)
			return 1
		}
		return 0
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		fmt.Fprintf(stderr, "serve: shutdown: %v\n", err)
		return 1
	}
	return 0
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
