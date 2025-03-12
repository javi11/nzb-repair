package repairnzb

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

type parfile struct {
	n    int
	file nzbparser.NzbFile
}

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

func par2repair(
	ctx context.Context,
	par2Exe string,
	tmpPath string,
) error {
	slog.InfoContext(ctx, "Starting repair process")

	var (
		par2FileName   string
		parameters     []string
		cmdReader      io.ReadCloser
		scanner        *bufio.Scanner
		parProgressBar *progressbar.ProgressBar
		err            error
	)

	exp, _ := regexp.Compile(`^.+\.par2`)
	if err = filepath.Walk(tmpPath, func(file string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && exp.MatchString(filepath.Base(info.Name())) {
			par2FileName = info.Name()
		}
		return nil
	}); err != nil {
		return err
	}

	// set parameters
	parameters = append(parameters, "r", "-q")
	// Delete par2 after repair
	parameters = append(parameters, "-p")
	// The filename of the par2 file
	parameters = append(parameters, filepath.Join(tmpPath, par2FileName))

	cmd := exec.Command(par2Exe, parameters...)
	slog.DebugContext(ctx, "Par command: %s", cmd.String())

	// create a pipe for the output of the program
	if cmdReader, err = cmd.StdoutPipe(); err != nil {
		return err
	}

	scanner = bufio.NewScanner(cmdReader)
	scanner.Split(scanLines)

	go func() {
		// progress bar
		parProgressBar = progressbar.NewOptions(int(100),
			progressbar.OptionSetDescription("INFO:    Repairing files    "),
			progressbar.OptionSetRenderBlankState(true),
			progressbar.OptionThrottle(time.Millisecond*100),
			progressbar.OptionShowElapsedTimeOnFinish(),
			progressbar.OptionOnCompletion(func() {
				// new line after progress bar
				fmt.Println()
			}),
		)

		for scanner.Scan() {
			output := strings.Trim(scanner.Text(), " \r\n")
			if output != "" && !strings.Contains(output, "%") {
				slog.DebugContext(ctx, fmt.Sprintf("PAR: %v", output))
			}

			exp := regexp.MustCompile(`(\d+)\.?\d*%`)
			if output != "" && exp.MatchString(output) {
				percentStr := exp.FindStringSubmatch(output)
				percentInt, _ := strconv.Atoi(percentStr[1])
				parProgressBar.Set(percentInt)
			}
		}

	}()

	if err = cmd.Run(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			if parProgressBar != nil {
				parProgressBar.Exit()
			}

			if errMsg, ok := par2ExitCodes[exitError.ExitCode()]; ok {
				return errors.New(errMsg)
			}

			return fmt.Errorf("unknown error")
		}
	}

	if parProgressBar != nil {
		parProgressBar.Finish()
	}

	slog.InfoContext(ctx, "Repair successful")

	return nil
}

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
