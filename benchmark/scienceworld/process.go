package scienceworld

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed bridge.py
var bridgeScript string

const maxBridgeLine = 2 << 20

type Config struct {
	Python          string
	TaskName        string
	Variation       int
	Simplifications string
	StepLimit       int
}

type Step struct {
	Observation     string           `json:"observation"`
	Info            map[string]any   `json:"info,omitempty"`
	Reward          float64          `json:"reward,omitempty"`
	Score           int              `json:"score,omitempty"`
	Completed       bool             `json:"completed,omitempty"`
	Moves           int              `json:"moves,omitempty"`
	TaskName        string           `json:"task_name,omitempty"`
	Variation       int              `json:"variation,omitempty"`
	Simplifications string           `json:"simplifications,omitempty"`
	GoalProgress    any              `json:"goal_progress,omitempty"`
	ValidActions    []map[string]any `json:"valid_actions,omitempty"`
	Error           string           `json:"error,omitempty"`
}

type processEnv struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	stderr  *limitedStderr
	tempDir string
	closed  bool
	last    Step
}

type limitedStderr struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *limitedStderr) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() < 32<<10 {
		remaining := (32 << 10) - w.buf.Len()
		if len(value) > remaining {
			_, _ = w.buf.Write(value[:remaining])
		} else {
			_, _ = w.buf.Write(value)
		}
	}
	return len(value), nil
}

func (w *limitedStderr) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSpace(w.buf.String())
}

func startEnvironment(ctx context.Context, config Config) (*processEnv, Step, error) {
	if strings.TrimSpace(config.TaskName) == "" {
		return nil, Step{}, errors.New("ScienceWorld task name is required")
	}
	if config.Variation < 0 {
		return nil, Step{}, errors.New("ScienceWorld variation must be non-negative")
	}
	if config.StepLimit <= 0 || config.StepLimit > 1000 {
		return nil, Step{}, errors.New("ScienceWorld step limit must be between 1 and 1000")
	}
	python := strings.TrimSpace(config.Python)
	if python == "" {
		python = "python3"
	}
	pythonPath, err := exec.LookPath(python)
	if err != nil {
		return nil, Step{}, fmt.Errorf("ScienceWorld requires Python 3.8+ executable %q: %w", python, err)
	}
	tempDir, err := os.MkdirTemp("", "paradox-scienceworld-")
	if err != nil {
		return nil, Step{}, err
	}
	if err := os.Chmod(tempDir, 0o700); err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, Step{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"task_name": config.TaskName, "variation": config.Variation,
		"simplifications": config.Simplifications, "step_limit": config.StepLimit,
	})
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, Step{}, err
	}
	stderr := &limitedStderr{}
	var command *exec.Cmd
	if prlimit, lookErr := exec.LookPath("prlimit"); lookErr == nil {
		command = exec.CommandContext(ctx, prlimit,
			"--as=4294967296", "--cpu=600", "--nofile=256", "--nproc=256", "--",
			pythonPath, "-I", "-c", bridgeScript)
	} else {
		command = exec.CommandContext(ctx, pythonPath, "-I", "-c", bridgeScript)
	}
	command.Dir = tempDir
	command.Env = scienceWorldEnvironment(tempDir)
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, Step{}, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, Step{}, err
	}
	if err := command.Start(); err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, Step{}, err
	}
	env := &processEnv{cmd: command, stdin: stdin, stdout: bufio.NewReaderSize(stdout, 64<<10), stderr: stderr, tempDir: tempDir}
	if _, err := stdin.Write(append(payload, '\n')); err != nil {
		_ = env.Close()
		return nil, Step{}, fmt.Errorf("start ScienceWorld bridge: %w", err)
	}
	var initial Step
	if err := env.readResponse(ctx, &initial); err != nil {
		message := stderr.String()
		_ = env.Close()
		if message != "" {
			return nil, Step{}, fmt.Errorf("ScienceWorld bridge initialization failed: %s", safeError(message))
		}
		return nil, Step{}, fmt.Errorf("ScienceWorld bridge initialization failed: %w", err)
	}
	if initial.Error != "" {
		_ = env.Close()
		return nil, Step{}, fmt.Errorf("ScienceWorld unavailable: %s", safeError(initial.Error))
	}
	if initial.Observation == "" {
		_ = env.Close()
		return nil, Step{}, errors.New("ScienceWorld bridge returned an empty initial observation")
	}
	env.last = initial
	return env, initial, nil
}

func scienceWorldEnvironment(tempDir string) []string {
	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + tempDir, "TMPDIR=" + tempDir,
		"LANG=C.UTF-8", "LC_ALL=C.UTF-8", "PYTHONNOUSERSITE=1", "PYTHONDONTWRITEBYTECODE=1",
	}
	if javaHome := strings.TrimSpace(os.Getenv("JAVA_HOME")); javaHome != "" {
		env = append(env, "JAVA_HOME="+javaHome)
	}
	return env
}

func (e *processEnv) Step(ctx context.Context, action string) (Step, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return Step{}, errors.New("ScienceWorld environment is closed")
	}
	if len(action) == 0 || len(action) > 1000 || strings.ContainsAny(action, "\r\n\x00") {
		return Step{}, errors.New("action must be 1-1000 characters and contain no line breaks or NUL bytes")
	}
	request, _ := json.Marshal(map[string]string{"operation": "step", "action": action})
	if _, err := e.stdin.Write(append(request, '\n')); err != nil {
		return Step{}, fmt.Errorf("send ScienceWorld action: %w", err)
	}
	var result Step
	if err := e.readResponse(ctx, &result); err != nil {
		return Step{}, err
	}
	if result.Error != "" {
		return Step{}, errors.New(safeError(result.Error))
	}
	if result.Observation == "" {
		return Step{}, errors.New("ScienceWorld returned an empty observation")
	}
	e.last = result
	return result, nil
}

func (e *processEnv) readResponse(ctx context.Context, target *Step) error {
	result := make(chan error, 1)
	go func() {
		line, err := readLimitedLine(e.stdout, maxBridgeLine)
		if err != nil {
			result <- err
			return
		}
		if err := json.Unmarshal([]byte(line), target); err != nil {
			result <- fmt.Errorf("decode ScienceWorld response: %w", err)
			return
		}
		result <- nil
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		if e.cmd.Process != nil {
			_ = syscall.Kill(-e.cmd.Process.Pid, syscall.SIGKILL)
		}
		return fmt.Errorf("ScienceWorld action timed out or was cancelled: %w", ctx.Err())
	}
}

func readLimitedLine(reader *bufio.Reader, limit int) ([]byte, error) {
	line := make([]byte, 0, 64<<10)
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > limit {
			return nil, fmt.Errorf("ScienceWorld response exceeded %d bytes", limit)
		}
		line = append(line, fragment...)
		if err == nil {
			return line, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

func (e *processEnv) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	if e.cmd.Process == nil {
		_ = os.RemoveAll(e.tempDir)
		return nil
	}
	_, _ = e.stdin.Write([]byte("{\"operation\":\"close\"}\n"))
	_ = e.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- e.cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-e.cmd.Process.Pid, syscall.SIGKILL)
		err = <-done
	}
	_ = os.RemoveAll(e.tempDir)
	if err != nil && !strings.Contains(err.Error(), "signal: killed") {
		return fmt.Errorf("close ScienceWorld bridge: %w", err)
	}
	return nil
}

func safeError(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	if len(value) > 500 {
		value = value[:500] + "..."
	}
	return value
}
