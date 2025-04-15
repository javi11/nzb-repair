package repairnzb

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Tensai75/nzbparser"
	"github.com/schollz/progressbar/v3"
)

// Allow mocking exec.CommandContext in tests
var execCommand = exec.CommandContext

// Par2Executor defines the interface for executing par2 commands.
type Par2Executor interface {
	Repair(ctx context.Context, tmpPath string) error
}

// Par2CmdExecutor implements Par2Executor using the command line.
type Par2CmdExecutor struct {
	ExePath string
}

var (
	parregexp = regexp.MustCompile(`(?i)(\.vol\d+\+(\d+))?\.par2$`)

	// par2 exit codes
	par2ExitCodes = map[int]string{
		0: "Success",
		1: "Repair possible",
		2: "Repair not possible",
		3: "Invalid command line arguments",
		4: "Insufficient critical data to verify",
		5: "Repair failed",
		6: "FileIO Error",
		7: "Logic Error",
		8: "Out of memory",
	}
)

func splitParWithRest(nfile *nzbparser.Nzb) (parFiles []nzbparser.NzbFile, restFiles []nzbparser.NzbFile) {
	parFiles = make([]nzbparser.NzbFile, 0)
	restFiles = make([]nzbparser.NzbFile, 0)

	for _, f := range nfile.Files {
		if parregexp.MatchString(f.Filename) {
			parFiles = append(parFiles, f)
		} else {
			restFiles = append(restFiles, f)
		}
	}

	return
}

// Repair executes the par2 command to repair files in the target folder.
func (p *Par2CmdExecutor) Repair(ctx context.Context, tmpPath string) error {
	slog.InfoContext(ctx, "Starting repair process", "executor", "Par2CmdExecutor")

	var (
		par2FileName   string
		parameters     []string
		parProgressBar *progressbar.ProgressBar
		err            error
	)

	par2Exe := p.ExePath
	if par2Exe == "" {
		par2Exe = "par2" // Default if path is empty
		slog.WarnContext(ctx, "Par2 executable path is empty, defaulting to 'par2'")
	}

	exp, _ := regexp.Compile(`^.+\.par2`)
	if err = filepath.Walk(tmpPath, func(file string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && exp.MatchString(filepath.Base(info.Name())) {
			// Use the first .par2 file found as the main input for par2
			// par2 should automatically find related files.
			if par2FileName == "" {
				par2FileName = info.Name()
			}
		}

		return nil
	}); err != nil {
		return fmt.Errorf("error finding .par2 file in %s: %w", tmpPath, err)
	}

	if par2FileName == "" {
		slog.WarnContext(ctx, "No .par2 file found in the temporary directory, skipping repair.", "path", tmpPath)
		// Depending on requirements, this might be an error or just a skip condition.
		// For now, assume it's okay to skip if no par2 file exists.
		return nil
	}

	slog.InfoContext(ctx, "Found par2 file for repair", "file", par2FileName)

	// set parameters
	parameters = append(parameters, "r", "-q")
	// Delete par2 after repair
	parameters = append(parameters, "-p")
	// The filename of the par2 file
	parameters = append(parameters, filepath.Join(tmpPath, par2FileName))

	// Use the package-level variable instead of calling exec.CommandContext directly
	cmd := execCommand(ctx, par2Exe, parameters...)
	cmd.Dir = tmpPath // Important: Run the command in the directory containing the files
	slog.DebugContext(ctx, fmt.Sprintf("Par command: %s in dir %s", cmd.String(), cmd.Dir))

	cmdErr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to get stderr pipe for par2: %w", err)
	}

	// create a pipe for the output of the program
	cmdReader, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe for par2: %w", err)
	}

	scanner := bufio.NewScanner(cmdReader)
	scanner.Split(scanLines)

	errScanner := bufio.NewScanner(cmdErr)
	errScanner.Split(scanLines)

	var stderrOutput strings.Builder
	go func() {
		for errScanner.Scan() {
			line := strings.TrimSpace(errScanner.Text())
			if line != "" {
				slog.DebugContext(ctx, "PAR2 STDERR:", "line", line)
				stderrOutput.WriteString(line + "\n")
			}
		}
	}()

	go func() {
		// Ensure parProgressBar is initialized before use
		parProgressBar = progressbar.NewOptions(100, // Use 100 as max for percentage
			progressbar.OptionSetDescription("INFO:    Repairing files    "),
			progressbar.OptionSetRenderBlankState(true),
			progressbar.OptionThrottle(time.Millisecond*100),
			progressbar.OptionShowElapsedTimeOnFinish(),
			progressbar.OptionClearOnFinish(),
			progressbar.OptionOnCompletion(func() {
				// new line after progress bar
				fmt.Println()
			}),
		)
		defer func() {
			_ = parProgressBar.Close() // Close the progress bar when done
		}()

		for scanner.Scan() {
			output := strings.Trim(scanner.Text(), " \r\n")
			if output != "" && !strings.Contains(output, "%") {
				slog.DebugContext(ctx, fmt.Sprintf("PAR2 STDOUT: %v", output))
			}

			exp := regexp.MustCompile(`(\d+)\.?\d*%`)
			if output != "" && exp.MatchString(output) {
				percentStr := exp.FindStringSubmatch(output)
				if len(percentStr) > 1 {
					percentInt, err := strconv.Atoi(percentStr[1])
					if err == nil {
						_ = parProgressBar.Set(percentInt)
					}
				}
			}
		}

	}()

	if err = cmd.Run(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			if parProgressBar != nil {
				_ = parProgressBar.Close() // Attempt to close/clear on error too
			}

			if errMsg, ok := par2ExitCodes[exitError.ExitCode()]; ok {
				// Specific known error codes from par2
				fullErrMsg := fmt.Sprintf("par2 exited with code %d: %s. Stderr: %s", exitError.ExitCode(), errMsg, stderrOutput.String())
				slog.ErrorContext(ctx, fullErrMsg)
				// Treat specific codes as potentially non-fatal or requiring different handling
				// For now, return all as errors, but could customize (e.g., ignore exit code 1 if repair was possible)
				return errors.New(fullErrMsg)
			}
			// Unknown exit code
			unknownErrMsg := fmt.Sprintf("par2 exited with unknown code %d. Stderr: %s", exitError.ExitCode(), stderrOutput.String())
			slog.ErrorContext(ctx, unknownErrMsg)
			return errors.New(unknownErrMsg)
		}
		// Error not related to exit code (e.g., command not found)
		return fmt.Errorf("failed to run par2 command '%s': %w. Stderr: %s", cmd.String(), err, stderrOutput.String())
	}

	if parProgressBar != nil {
		_ = parProgressBar.Finish() // Ensure finish is called on success
	}

	slog.InfoContext(ctx, "Par2 repair completed successfully")

	return nil
}

// scanLines is a helper for bufio.Scanner to split lines correctly
func scanLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}

	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		if data[i] == '\n' {
			// We have a line terminated by single newline.
			return i + 1, data[0:i], nil
		}

		advance = i + 1
		if len(data) > i+1 && data[i+1] == '\n' {
			advance += 1
		}

		return advance, data[0:i], nil
	}

	// If we're at EOF, we have a final, non-terminated line. Return it.
	if atEOF {
		return len(data), data, nil
	}

	// Request more data.
	return 0, nil, nil
}
