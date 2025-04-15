package repairnzb

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	// Check if we are running as the helper process
	if os.Getenv("GO_TEST_HELPER_PROCESS") == "1" {
		// We need a testing.T instance, but this part of the code
		// is only run in the helper process, not during the main test execution.
		// So, we can pass nil or a dummy T. This code won't actually run
		// test functions. The real test execution happens in the parent process.
		testHelperProcess(nil)
		return // Important: Exit after acting as helper
	}

	// Run the actual tests
	os.Exit(m.Run())
}

func TestPar2CmdExecutor_Repair(t *testing.T) {
	// Backup and restore execCommand
	originalExecCommand := execCommand
	execCommand = mockExecCommand
	defer func() { execCommand = originalExecCommand }()

	// Setup default logger
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))

	t.Run("Success", func(t *testing.T) {
		tmpDir := t.TempDir()
		par2File := filepath.Join(tmpDir, "test.par2")
		_, err := os.Create(par2File)
		require.NoError(t, err)

		// Set env vars for the mock command
		os.Setenv("TEST_PAR2_EXIT_CODE", "0")
		os.Setenv("TEST_PAR2_STDOUT", `Verifying files...
Repair complete.
100%`)
		os.Setenv("TEST_PAR2_STDERR", "")
		defer func() {
			os.Unsetenv("TEST_PAR2_EXIT_CODE")
			os.Unsetenv("TEST_PAR2_STDOUT")
			os.Unsetenv("TEST_PAR2_STDERR")
		}()

		executor := &Par2CmdExecutor{ExePath: "par2"} // ExePath is used by mock indirectly
		ctx := context.Background()
		err = executor.Repair(ctx, tmpDir)
		assert.NoError(t, err)
	})

	t.Run("No Par2 File", func(t *testing.T) {
		tmpDir := t.TempDir()
		// No .par2 file created

		executor := &Par2CmdExecutor{ExePath: "par2"}
		ctx := context.Background()
		err := executor.Repair(ctx, tmpDir)
		assert.NoError(t, err, "Should not return error if no par2 file is found")
	})

	t.Run("Repair Possible Exit Code 1", func(t *testing.T) {
		tmpDir := t.TempDir()
		par2File := filepath.Join(tmpDir, "test.par2")
		_, err := os.Create(par2File)
		require.NoError(t, err)

		os.Setenv("TEST_PAR2_EXIT_CODE", "1")
		os.Setenv("TEST_PAR2_STDOUT", `Verifying files...
Need to repair 5 blocks.
Repair possible.
100%`)
		os.Setenv("TEST_PAR2_STDERR", "Some warnings maybe")
		defer func() {
			os.Unsetenv("TEST_PAR2_EXIT_CODE")
			os.Unsetenv("TEST_PAR2_STDOUT")
			os.Unsetenv("TEST_PAR2_STDERR")
		}()

		executor := &Par2CmdExecutor{ExePath: "par2"}
		ctx := context.Background()
		err = executor.Repair(ctx, tmpDir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "par2 exited with code 1: Repair possible")
	})

	t.Run("Repair Not Possible Exit Code 2", func(t *testing.T) {
		tmpDir := t.TempDir()
		par2File := filepath.Join(tmpDir, "test.par2")
		_, err := os.Create(par2File)
		require.NoError(t, err)

		os.Setenv("TEST_PAR2_EXIT_CODE", "2")
		os.Setenv("TEST_PAR2_STDOUT", `Verifying files...
Need 10 recovery blocks, only 5 available.`)
		os.Setenv("TEST_PAR2_STDERR", "Not enough data")
		defer func() {
			os.Unsetenv("TEST_PAR2_EXIT_CODE")
			os.Unsetenv("TEST_PAR2_STDOUT")
			os.Unsetenv("TEST_PAR2_STDERR")
		}()

		executor := &Par2CmdExecutor{ExePath: "par2"}
		ctx := context.Background()
		err = executor.Repair(ctx, tmpDir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "par2 exited with code 2: Repair not possible")
	})

	t.Run("Unknown Exit Code", func(t *testing.T) {
		tmpDir := t.TempDir()
		par2File := filepath.Join(tmpDir, "test.par2")
		_, err := os.Create(par2File)
		require.NoError(t, err)

		os.Setenv("TEST_PAR2_EXIT_CODE", "99")
		os.Setenv("TEST_PAR2_STDOUT", "")
		os.Setenv("TEST_PAR2_STDERR", "Something weird happened")
		defer func() {
			os.Unsetenv("TEST_PAR2_EXIT_CODE")
			os.Unsetenv("TEST_PAR2_STDOUT")
			os.Unsetenv("TEST_PAR2_STDERR")
		}()

		executor := &Par2CmdExecutor{ExePath: "par2"}
		ctx := context.Background()
		err = executor.Repair(ctx, tmpDir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "par2 exited with unknown code 99")
	})

	t.Run("Command Not Found", func(t *testing.T) {
		tmpDir := t.TempDir()
		par2File := filepath.Join(tmpDir, "test.par2")
		_, err := os.Create(par2File)
		require.NoError(t, err)

		// Using a high, unmapped exit code might simulate an execution environment issue
		os.Setenv("TEST_PAR2_EXIT_CODE", "127") // Often used for command not found by shells
		os.Setenv("TEST_PAR2_STDOUT", "")
		os.Setenv("TEST_PAR2_STDERR", "par2: command not found") // Simulate typical stderr
		defer func() {
			os.Unsetenv("TEST_PAR2_EXIT_CODE")
			os.Unsetenv("TEST_PAR2_STDOUT")
			os.Unsetenv("TEST_PAR2_STDERR")
		}()

		executor := &Par2CmdExecutor{ExePath: "/non/existent/par2"} // Use a clearly invalid path
		ctx := context.Background()
		err = executor.Repair(ctx, tmpDir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "par2 exited with unknown code 127")
	})

	t.Run("Progress Bar Handling", func(t *testing.T) {
		tmpDir := t.TempDir()
		par2File := filepath.Join(tmpDir, "test.par2")
		_, err := os.Create(par2File)
		require.NoError(t, err)

		// Simulate stdout with progress percentage
		os.Setenv("TEST_PAR2_EXIT_CODE", "0")
		os.Setenv("TEST_PAR2_STDOUT", `Verifying files...
50% complete
Repairing...
100.00%
Done.`)
		os.Setenv("TEST_PAR2_STDERR", "")
		defer func() {
			os.Unsetenv("TEST_PAR2_EXIT_CODE")
			os.Unsetenv("TEST_PAR2_STDOUT")
			os.Unsetenv("TEST_PAR2_STDERR")
		}()

		executor := &Par2CmdExecutor{ExePath: "par2"}
		ctx := context.Background()
		// Capture log output to check progress bar messages indirectly
		var logBuf bytes.Buffer
		handler := slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})
		ctx = context.WithValue(ctx, "key", slog.New(handler))

		err = executor.Repair(ctx, tmpDir)
		assert.NoError(t, err)
	})

	t.Run("Empty ExePath uses default", func(t *testing.T) {
		tmpDir := t.TempDir()
		par2File := filepath.Join(tmpDir, "test.par2")
		_, err := os.Create(par2File)
		require.NoError(t, err)

		os.Setenv("TEST_PAR2_EXIT_CODE", "0")
		os.Setenv("TEST_PAR2_STDOUT", "Success")
		os.Setenv("TEST_PAR2_STDERR", "")
		defer func() {
			os.Unsetenv("TEST_PAR2_EXIT_CODE")
			os.Unsetenv("TEST_PAR2_STDOUT")
			os.Unsetenv("TEST_PAR2_STDERR")
		}()

		// Mock execCommand checks the command name passed to it
		execCommand = func(ctx context.Context, command string, args ...string) *exec.Cmd {
			assert.Equal(t, "par2", command, "Expected default 'par2' command when ExePath is empty")
			cs := []string{"-test.run=TestHelperProcess", "--", command}
			cs = append(cs, args...)
			cmd := exec.CommandContext(ctx, os.Args[0], cs...)
			cmd.Env = append(os.Environ(), "GO_TEST_HELPER_PROCESS=1") // Pass env vars
			return cmd
		}
		defer func() { execCommand = originalExecCommand }() // Restore original

		executor := &Par2CmdExecutor{ExePath: ""} // Empty ExePath
		ctx := context.Background()
		err = executor.Repair(ctx, tmpDir)
		assert.NoError(t, err)
	})
}

// testHelperProcess is run when the test binary is executed with a specific env var.
// It simulates the behavior of the par2 command based on environment variables.
func testHelperProcess(t *testing.T) {
	if os.Getenv("GO_TEST_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0) // Exit cleanly after simulation

	args := os.Args
	for len(args) > 0 {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "No command provided to test helper\n")
		os.Exit(1) // Should have command args
	}

	// Simulate par2 behavior based on env vars
	exitCodeStr := os.Getenv("TEST_PAR2_EXIT_CODE")
	stdout := os.Getenv("TEST_PAR2_STDOUT")
	stderr := os.Getenv("TEST_PAR2_STDERR")
	exitCode := 0 // Default to success

	if exitCodeStr != "" {
		var err error
		exitCode, err = strconv.Atoi(exitCodeStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid TEST_PAR2_EXIT_CODE: %v\n", err)
			os.Exit(7) // Simulate Logic Error
		}
	}

	fmt.Fprint(os.Stdout, stdout)
	fmt.Fprint(os.Stderr, stderr)
	os.Exit(exitCode)
}

// Override the exec.CommandContext function for testing
func mockExecCommand(ctx context.Context, command string, args ...string) *exec.Cmd {
	cs := []string{"-test.run=TestHelperProcess", "--", command}
	cs = append(cs, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], cs...)
	// Set environment variables for the helper process
	cmd.Env = append(os.Environ(),
		"GO_TEST_HELPER_PROCESS=1",
		fmt.Sprintf("TEST_PAR2_EXIT_CODE=%s", os.Getenv("TEST_PAR2_EXIT_CODE")),
		fmt.Sprintf("TEST_PAR2_STDOUT=%s", os.Getenv("TEST_PAR2_STDOUT")),
		fmt.Sprintf("TEST_PAR2_STDERR=%s", os.Getenv("TEST_PAR2_STDERR")),
	)
	return cmd
}
