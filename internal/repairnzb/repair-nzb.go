package repairnzb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tensai75/nzbparser"
	"github.com/javi11/nntppool"
	"github.com/javi11/nzb-repair/internal/config"
	"github.com/k0kubun/go-ansi"
	"github.com/schollz/progressbar/v3"
	"github.com/sourcegraph/conc/pool"
)

func RepairNzb(
	ctx context.Context,
	config config.Config,
	downloadPool nntppool.UsenetConnectionPool,
	uploadPool nntppool.UsenetConnectionPool,
	nzbFile string,
) error {
	content, err := os.Open(nzbFile)
	if err != nil {
		return err
	}

	nzb, err := nzbparser.Parse(content)
	if err != nil {
		return err
	}

	parFiles, restFiles := splitParWithRest(nzb)
	if len(parFiles) == 0 {
		slog.InfoContext(ctx, "No par2 files found, stopping repair.")
		return nil
	}

	brokenSegments := make([]nzbparser.NzbSegment, 0)
	brokenSegmentCh := make(chan nzbparser.NzbSegment, 100)

	wg := &sync.WaitGroup{}
	defer func() {
		close(brokenSegmentCh)
		wg.Wait()
	}()

	// goroutine to listen for broken segments
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case s, ok := <-brokenSegmentCh:
				if !ok {
					return
				}

				brokenSegments = append(brokenSegments, s)
			}
		}
	}()

	if len(restFiles) == 0 {
		slog.InfoContext(ctx, "No files to repair, stopping repair.")

		return nil
	}

	firstFile := restFiles[0]
	tmpFolder := filepath.Join(config.DownloadFolder, firstFile.Basefilename)
	if err := os.MkdirAll(tmpFolder, 0755); err != nil {
		if !errors.Is(err, os.ErrExist) {
			slog.With("err", err).ErrorContext(ctx, "failed to create folder")

			return err
		}
	}

	// Download files
	startTime := time.Now()
	for _, f := range restFiles {
		if ctx.Err() != nil {
			slog.With("err", err).ErrorContext(ctx, "repair canceled")

			return nil
		}

		err := downloadWorker(ctx, config, downloadPool, f, brokenSegmentCh, tmpFolder)
		if err != nil {
			slog.With("err", err).ErrorContext(ctx, "failed to download file")
		}

	}

	if ctx.Err() != nil {
		slog.With("err", err).ErrorContext(ctx, "repair canceled")

		return nil
	}

	elapsed := time.Since(startTime)

	slog.InfoContext(ctx, fmt.Sprintf("%d files downloaded in %s", len(restFiles), elapsed))

	if len(brokenSegments) == 0 {
		slog.InfoContext(ctx, "No broken segments found, stopping repair.")

		return nil
	}

	// Download par2 files
	slog.InfoContext(ctx, fmt.Sprintf("%d broken segments found. Downloading par2 files", len(brokenSegments)))
	for _, f := range parFiles {
		if ctx.Err() != nil {
			return nil
		}

		err := downloadWorker(ctx, config, downloadPool, f, nil, tmpFolder)
		if err != nil {
			slog.With("err", err).InfoContext(ctx, "failed to download par2 file, cancelling repair")
		}
	}

	err = par2repair(ctx, config.Par2Exe, tmpFolder)
	if err != nil {
		slog.With("err", err).ErrorContext(ctx, "failed to repair files")
	}

	return nil
}

func downloadWorker(
	ctx context.Context,
	config config.Config,
	downloadPool nntppool.UsenetConnectionPool,
	file nzbparser.NzbFile,
	brokenSegmentCh chan<- nzbparser.NzbSegment,
	downloadDir string,
) error {
	brokenSegmentCounter := atomic.Int64{}

	p := pool.New().WithContext(ctx).
		WithMaxGoroutines(config.DownloadWorkers).
		WithCancelOnError()

	slog.InfoContext(ctx, fmt.Sprintf("Starting downloading file %s", file.Filename))

	fileWriter, err := os.Create(filepath.Join(downloadDir, file.Filename))
	if err != nil {
		slog.With("err", err).ErrorContext(ctx, "failed to create file: %v")

		return fmt.Errorf("failed to create file: %w", err)
	}

	bar := progressbar.NewOptions(int(file.Bytes),
		progressbar.OptionSetWriter(ansi.NewAnsiStdout()), //you should install "github.com/k0kubun/go-ansi"
		progressbar.OptionEnableColorCodes(true),
		progressbar.OptionSetWidth(15),
		progressbar.OptionShowBytes(true),
		progressbar.OptionShowTotalBytes(true),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "[green]=[reset]",
			SaucerHead:    "[green]>[reset]",
			SaucerPadding: " ",
			BarStart:      "[",
			BarEnd:        "]",
		}))

	c, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, s := range file.Segments {
		select {
		case <-c.Done():
			return nil
		case <-ctx.Done():
			return nil
		default:
			p.Go(func(c context.Context) error {
				buff := bytes.NewBuffer(make([]byte, 0))
				if _, err := downloadPool.Body(c, s.Id, buff, file.Groups); err != nil {
					if errors.Is(err, nntppool.ErrArticleNotFoundInProviders) {
						if brokenSegmentCh != nil {
							slog.DebugContext(ctx, fmt.Sprintf("segment %s not found, sending for repair: %v", s.Id, err))

							brokenSegmentCh <- s
							brokenSegmentCounter.Add(1)

							//bs := brokenSegmentCounter.Load()

							/* if float64(bs)/float64(len(file.Segments)) > 0.5 {
								cancel()

								slog.ErrorContext(ctx, fmt.Sprintf("too many broken segments (>50%%): %d/%d, cancelling", bs, len(file.Segments)))

								return fmt.Errorf("too many broken segments (>50%%): %d/%d, cancelling", bs, len(file.Segments))
							} */
						} else if !errors.Is(err, context.Canceled) {
							return fmt.Errorf("segment %v not found", s.Id)
						}

						return nil
					}

					if errors.Is(err, context.Canceled) {
						return nil
					}

					slog.ErrorContext(ctx, fmt.Sprintf("failed to download segment %s canceling the repair: %v", s.Id, err))
					cancel()

					return err
				}

				start := (s.Number - 1) * buff.Len()
				fileWriter.WriteAt(buff.Bytes(), int64(start))
				bar.Add(s.Bytes)

				return nil
			})
		}
	}

	if err := p.Wait(); err != nil {
		return err
	}

	fmt.Println() // Add newline after progress is complete
	return fileWriter.Close()
}
