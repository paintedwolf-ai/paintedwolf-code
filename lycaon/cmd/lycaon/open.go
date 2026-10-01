package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/lycaon/lycaon/internal/clisocket"
	"github.com/lycaon/lycaon/internal/version"
)

const (
	coldStartTimeout = 20 * time.Second
	coldStartPoll    = 150 * time.Millisecond
	requestTimeout   = 10 * time.Second
)

func runOpen(ctx context.Context, args []string) error {
	target := ""
	req := clisocket.Request{Op: clisocket.OpOpen}
	for _, arg := range args {
		switch {
		case arg == "--new":
			req.New = true
		case strings.HasPrefix(arg, "-"):
			return fmt.Errorf("unknown flag %q", arg)
		case target != "":
			return fmt.Errorf("open takes one folder or project name")
		default:
			target = arg
		}
	}
	if target == "" {
		target = "."
	}

	// Resolve relative paths before crossing into the engine process.
	resolved, err := absolutizeIfPath(target)
	if err != nil {
		return err
	}
	req.Target = resolved

	resp, err := request(ctx, req)
	if err != nil {
		return err
	}
	return reportOpen(resp)
}

func reportOpen(resp clisocket.Response) error {
	switch resp.Status {
	case clisocket.StatusAmbiguous:
		fmt.Fprintln(os.Stderr, resp.Message)
		printProjects(os.Stderr, resp.Projects)
		return fmt.Errorf("ambiguous project name")
	case clisocket.StatusError:
		return fmt.Errorf("%s", resp.Message)
	}
	label := ""
	if len(resp.Projects) == 1 {
		label = resp.Projects[0].Name
	}
	if resp.Action == "create" {
		fmt.Println("Confirm the new project in Painted Wolf Code.")
		return nil
	}
	if label != "" {
		fmt.Printf("Opening %s\n", label)
	}
	return nil
}

// absolutizeIfPath turns a folder argument into an absolute path and leaves a
// project name untouched.
func absolutizeIfPath(target string) (string, error) {
	expanded, err := expandHome(target)
	if err != nil {
		return "", err
	}
	if !clisocket.LooksLikePath(expanded) {
		return expanded, nil
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", target, err)
	}
	return abs, nil
}

// expandHome handles quoted paths whose tilde was not expanded.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

func runLs(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("usage: pw ls")
	}
	resp, err := request(ctx, clisocket.Request{Op: clisocket.OpList})
	if err != nil {
		return err
	}
	if resp.Status != clisocket.StatusOK {
		return fmt.Errorf("%s", resp.Message)
	}
	printProjects(os.Stdout, resp.Projects)
	// Refresh the completion cache so tab completion never dials the socket,
	// which would hang while the app is cold.
	if err := writeCompletionCache(resp.Projects); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not refresh completion cache: %v\n", err)
	}
	return nil
}

func printProjects(w *os.File, rows []clisocket.ProjectRow) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No projects yet — `pw open <folder>` makes one.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\n", row.Name, row.Path)
	}
	_ = tw.Flush()
}

func request(parent context.Context, req clisocket.Request) (clisocket.Response, error) {
	ctx, cancel := context.WithTimeout(parent, coldStartTimeout+requestTimeout)
	defer cancel()
	conn, err := dialOrStart(ctx)
	if err != nil {
		return clisocket.Response{}, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(requestTimeout))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return clisocket.Response{}, fmt.Errorf("send request: %w", err)
	}
	var resp clisocket.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return clisocket.Response{}, fmt.Errorf("read reply: %w", err)
	}
	return resp, nil
}

// dialOrStart connects to a running engine, cold-starting the app if there is none.
func dialOrStart(ctx context.Context) (net.Conn, error) {
	path, err := clisocket.SocketPath()
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{}
	if conn, err := dialer.DialContext(ctx, "unix", path); err == nil {
		return conn, nil
	}
	if err := startApp(ctx); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(coldStartTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(coldStartPoll):
		}
		if conn, err := dialer.DialContext(ctx, "unix", path); err == nil {
			return conn, nil
		}
	}
	return nil, fmt.Errorf("app did not finish starting within %s", coldStartTimeout)
}

func startApp(ctx context.Context) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("app is not running")
	}
	out, err := exec.CommandContext(ctx, "open", "-b", version.BundleID).CombinedOutput() //nolint:gosec // G204 — fixed program and a compile-time constant bundle identifier
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("could not start Painted Wolf Code: %s", detail)
	}
	return nil
}
