package analyzer

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Spawn starts the analyzer as a child process on a free loopback port and waits until it answers. It runs
// `uv run --project <dir> python -m photosort_analyzer` when uv is on PATH, else <dir>/.venv/bin/python. The process
// is killed when ctx is done; stop waits for it.
func Spawn(ctx context.Context, dir string, env []string) (c *Client, stop func(), err error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	args := []string{"-m", "photosort_analyzer", "--port", fmt.Sprint(port)}
	var cmd *exec.Cmd
	if uv, err := exec.LookPath("uv"); err == nil {
		cmd = exec.CommandContext(ctx, uv, append([]string{"run", "--quiet", "--project", dir, "python"}, args...)...)
	} else {
		cmd = exec.CommandContext(ctx, filepath.Join(dir, ".venv", "bin", "python"), args...)
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("starting the analyzer from %s: %w", dir, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stop = func() {
		if cmd.Process != nil {
			cmd.Process.Signal(os.Interrupt)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	}
	c = New(fmt.Sprintf("http://127.0.0.1:%d", port))
	deadline := time.Now().Add(2 * time.Minute) // first run may create the venv
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			return nil, nil, fmt.Errorf("the analyzer exited on start: %v", err)
		case <-time.After(250 * time.Millisecond):
		}
		hctx, cancel := context.WithTimeout(ctx, time.Second)
		_, err := c.Health(hctx)
		cancel()
		if err == nil {
			return c, stop, nil
		}
	}
	stop()
	return nil, nil, fmt.Errorf("the analyzer did not answer on port %d", port)
}
