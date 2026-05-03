// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/skorokithakis/graphe/internal/server"
	"github.com/spf13/cobra"
)

const (
	defaultHost     = "127.0.0.1"
	defaultPort     = 7290
	shutdownTimeout = 5 * time.Second
)

var (
	serveHost string
	servePort int
)

func init() {
	serveCommand.Flags().StringVar(&serveHost, "host", defaultHost, "Host or IP to bind the server to")
	serveCommand.Flags().IntVar(&servePort, "port", defaultPort, "TCP port to listen on")
	rootCommand.AddCommand(serveCommand)
}

var serveCommand = &cobra.Command{
	Use:   "serve <file.md>",
	Short: "Serve a markdown post in the browser with live reload.",
	Long: `serve renders the given markdown file as a Tufte-style post and serves it
over HTTP. By default it binds to 127.0.0.1:7290; override with --host and
--port. The page reloads automatically in the browser whenever the markdown
file or its review sidecar changes on disk.

If a <stem>-review.json sidecar exists next to the markdown file, it is loaded
automatically and its comments are rendered as margin notes. No flag is needed.

Press Ctrl-C to stop the server cleanly.`,
	Example: `  graphe serve path/to/post.md
  graphe serve ~/drafts/essay.md --port 8080
  graphe serve essay.md --host 0.0.0.0 --port 7290`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mdPath := args[0]

		absPath, err := filepath.Abs(mdPath)
		if err != nil {
			return fmt.Errorf("resolving path: %w", err)
		}

		grapheServer, err := server.New(absPath)
		if err != nil {
			return fmt.Errorf("initialising server: %w", err)
		}

		address := net.JoinHostPort(serveHost, fmt.Sprintf("%d", servePort))

		listener, err := net.Listen("tcp", address)
		if err != nil {
			// net.Listen returns a *net.OpError whose Err field is a *os.SyscallError
			// wrapping syscall.EADDRINUSE when the port is taken. We check for that
			// specifically so we can emit a clear message instead of the raw Go error.
			var opError *net.OpError
			if errors.As(err, &opError) {
				var syscallError *os.SyscallError
				if errors.As(opError.Err, &syscallError) && errors.Is(syscallError.Err, syscall.EADDRINUSE) {
					fmt.Fprintf(os.Stderr, "port %d on %s already in use\n", servePort, serveHost)
					os.Exit(1)
				}
			}
			return fmt.Errorf("listening on %s: %w", address, err)
		}

		fmt.Printf("graphe serving %s at http://%s\n", absPath, address)

		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		go func() {
			if err := grapheServer.Watch(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "watcher error: %v\n", err)
			}
		}()

		httpServer := &http.Server{Handler: grapheServer.Handler()}

		// Serve in a goroutine so we can wait for the signal below.
		serveError := make(chan error, 1)
		go func() {
			// Serve returns http.ErrServerClosed after Shutdown is called, which is
			// the expected path; any other error is unexpected and worth surfacing.
			if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serveError <- err
			}
			close(serveError)
		}()

		select {
		case err := <-serveError:
			if err != nil {
				return fmt.Errorf("http server: %w", err)
			}
			return nil
		case <-ctx.Done():
		}

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutting down http server: %w", err)
		}

		return nil
	},
}
