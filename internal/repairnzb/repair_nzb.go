package repairnzb

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"github.com/mnightingale/rapidyenc"
	"github.com/schollz/progressbar/v3"
	"github.com/sourcegraph/conc/pool"
)

func RepairNzb(
	ctx context.Context,
	cfg config.Config,
	downloadPool nntppool.UsenetConnectionPool,
	uploadPool nntppool.UsenetConnectionPool,
	par2Executor Par2Executor,
	nzbFile string,
	outputFile string,
	tmpDir string,
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
		slog.InfoContext(ctx, "No par2 files found in NZB, stopping repair.")
		return nil
	}

	brokenSegments := make(map[*nzbparser.NzbFile][]brokenSegment, 0)
	brokenSegmentCh := make(chan brokenSegment, 100)

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

				if _, ok := brokenSegments[s.file]; !ok {
					brokenSegments[s.file] = make([]brokenSegment, 0)
				}

				brokenSegments[s.file] = append(brokenSegments[s.file], s)
			}
		}
	}()

	if len(restFiles) == 0 {
		slog.InfoContext(ctx, "No files to repair, stopping repair.")

		return nil
	}

	firstFile := restFiles[0]
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		if !errors.Is(err, os.ErrExist) {
			slog.With("err", err).ErrorContext(ctx, "failed to ensure temp folder exists")
			return err
		}
	}

	defer func() {
		slog.InfoContext(ctx, "Cleaning up temporary directory", "path", tmpDir)
		if err := os.RemoveAll(tmpDir); err != nil {
			slog.ErrorContext(ctx, "Failed to clean up temporary directory", "path", tmpDir, "error", err)
		}
	}()

	// Download files
	startTime := time.Now()
	for _, f := range restFiles {
		if ctx.Err() != nil {
			slog.With("err", err).ErrorContext(ctx, "repair canceled")

			return nil
		}

		err := downloadWorker(ctx, cfg, downloadPool, f, brokenSegmentCh, tmpDir)
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

		err := downloadWorker(ctx, cfg, downloadPool, f, nil, tmpDir)
		if err != nil {
			slog.With("err", err).InfoContext(ctx, "failed to download par2 file, cancelling repair")
		}
	}

	err = par2Executor.Repair(ctx, tmpDir)
	if err != nil {
		slog.With("err", err).ErrorContext(ctx, "failed to repair files")
	}

	// Upload repaired files
	startTime = time.Now()

	err = replaceBrokenSegments(ctx, brokenSegments, tmpDir, cfg, uploadPool, nzb)
	if err != nil {
		slog.With("err", err).ErrorContext(ctx, "failed to upload repaired files")

		return err
	}

	// write the repaired nzb file
	var nzbFileName string
	if outputFile != "" {
		nzbFileName = outputFile
	} else {
		inputFileFolder := filepath.Dir(nzbFile)
		nzbFileName = filepath.Join(inputFileFolder, fmt.Sprintf("%s.repaired.nzb", firstFile.Basefilename))
	}

	// Ensure output directory exists
	outputDirPath := filepath.Dir(nzbFileName)
	if err := os.MkdirAll(outputDirPath, 0755); err != nil {
		if !errors.Is(err, os.ErrExist) {
			slog.With("err", err).ErrorContext(ctx, "failed to create output directory")
			return err
		}
	}

	b, err := nzbparser.Write(nzb)
	if err != nil {
		slog.With("err", err).ErrorContext(ctx, "failed to write repaired nzb file")

		return err
	}

	nzbFileHandle, err := os.Create(nzbFileName)
	if err != nil {
		slog.With("err", err).ErrorContext(ctx, "failed to create repaired nzb file")

		return err
	}

	defer func() {
		_ = nzbFileHandle.Close()
	}()

	if _, err := nzbFileHandle.Write(b); err != nil {
		slog.With("err", err).ErrorContext(ctx, "failed to write repaired nzb file")

		return err
	}

	slog.InfoContext(ctx, fmt.Sprintf("Repaired nzb file written to %s", nzbFileName))
	slog.InfoContext(ctx, fmt.Sprintf("%d broken segments uploaded in %s", len(brokenSegments), time.Since(startTime)))
	slog.InfoContext(ctx, "Repair completed successfully")

	return nil
}

func replaceBrokenSegments(
	ctx context.Context,
	brokenSegments map[*nzbparser.NzbFile][]brokenSegment,
	tmpFolder string,
	cfg config.Config,
	uploadPool nntppool.UsenetConnectionPool,
	nzb *nzbparser.Nzb,
) error {
	encoder := rapidyenc.NewEncoder()

	for nzbFile, bs := range brokenSegments {
		if ctx.Err() != nil {
			slog.ErrorContext(ctx, "repair canceled")

			return nil
		}

		f, err := os.Open(filepath.Join(tmpFolder, nzbFile.Filename))
		if err != nil {
			slog.With("err", err).ErrorContext(ctx, "failed to open file")

			return err
		}

		fs, err := f.Stat()
		if err != nil {
			slog.With("err", err).ErrorContext(ctx, "failed to get file info")

			return err
		}

		fileSize := fs.Size()

		p := pool.New().WithContext(ctx).
			WithMaxGoroutines(cfg.UploadWorkers).
			WithCancelOnError()

		for _, s := range bs {
			p.Go(func(ctx context.Context) error {
				if ctx.Err() != nil {
					slog.With("err", err).ErrorContext(ctx, "repair canceled")

					return nil
				}

				// Get the segment from the file
				buff := make([]byte, s.segment.Bytes)
				_, err := f.ReadAt(buff, int64((s.segment.Number-1)*s.segment.Bytes))
				if err != nil {
					slog.With("err", err).ErrorContext(ctx, "failed to read segment")

					return err
				}

				partSize := int64(s.segment.Bytes)
				date := time.UnixMilli(int64(nzbFile.Date))

				subject := fmt.Sprintf("[%v/%v] %v - \"\" yEnc (%v/%v)", s.file.Number, nzb.TotalFiles, s.file.Filename, int64(s.segment.Number), s.file.TotalSegments)

				var fName string

				if cfg.Upload.ObfuscationPolicy == config.ObfuscationPolicyNone {
					fName = s.file.Filename
				} else {
					fName = rand.Text()
					subject = rand.Text()
				}

				msgId := generateRandomMessageID()

				ar := articleData{
					PartNum:   int64(s.segment.Number),
					PartTotal: fileSize / partSize,
					PartSize:  partSize,
					PartBegin: int64((s.segment.Number - 1) * s.segment.Bytes),
					PartEnd:   int64(s.segment.Number * s.segment.Bytes),
					FileNum:   s.file.Number,
					FileTotal: 1,
					FileSize:  fileSize,
					Subject:   subject,
					Poster:    nzbFile.Poster,
					Groups:    nzbFile.Groups,
					Filename:  fName,
					Date:      &date,
					body:      buff,
					MsgId:     msgId,
				}

				r, err := ar.EncodeBytes(encoder)
				if err != nil {
					slog.With("err", err).ErrorContext(ctx, "failed to encode segment")

					return err
				}

				// Upload the segment
				err = uploadPool.Post(ctx, r)
				if err != nil {
					slog.With("err", err).ErrorContext(ctx, "failed to upload segment")

					return err
				}

				slog.InfoContext(ctx, fmt.Sprintf("Uploaded segment %s", s.segment.Id))
				nzbFile.Segments[s.segment.Number-1].Id = msgId

				return nil
			})
		}

		if err := p.Wait(); err != nil {
			slog.With("err", err).ErrorContext(ctx, "failed to upload segments")

			return err
		}

		slog.InfoContext(ctx, fmt.Sprintf("Uploaded %d segments for file %s", len(bs), nzbFile.Filename))

		// Replace the original broken file in the nzb with the repaired version
		for i, f := range nzb.Files {
			if f.Filename == nzbFile.Filename {
				nzb.Files[i] = *nzbFile
				break
			}
		}
	}

	return nil
}

func downloadWorker(
	ctx context.Context,
	config config.Config,
	downloadPool nntppool.UsenetConnectionPool,
	file nzbparser.NzbFile,
	brokenSegmentCh chan<- brokenSegment,
	tmpFolder string,
) error {
	brokenSegmentCounter := atomic.Int64{}

	p := pool.New().WithContext(ctx).
		WithMaxGoroutines(config.DownloadWorkers).
		WithCancelOnError()

	slog.InfoContext(ctx, fmt.Sprintf("Starting downloading file %s", file.Filename))

	filePath := filepath.Join(tmpFolder, file.Filename)

	// Check if file exists
	if _, err := os.Stat(filePath); err == nil {
		slog.InfoContext(ctx, fmt.Sprintf("File %s already exists, skipping download", file.Filename))
		return nil
	}

	fileWriter, err := os.Create(filePath)
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

	once := sync.Once{}

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

							brokenSegmentCh <- brokenSegment{
								segment: &s,
								file:    &file,
							}
							brokenSegmentCounter.Add(1)

							// Recalculate segment size for wrong segment sizes
							once.Do(func() {
								for _, s := range file.Segments {
									s.Bytes = buff.Len()
								}
							})
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

				_, err = fileWriter.WriteAt(buff.Bytes(), int64(start))
				if err != nil {
					slog.With("err", err).ErrorContext(ctx, "failed to write segment")

					return err
				}

				_ = bar.Add(s.Bytes)

				return nil
			})
		}
	}

	if err := p.Wait(); err != nil {
		return err
	}

	return fileWriter.Close()
}
