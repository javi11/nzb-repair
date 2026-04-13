package repairnzb

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mnightingale/rapidyenc"
)

type articleData struct {
	PartNum       int64             `json:"part_num"`
	PartTotal     int64             `json:"part_total"`
	PartSize      int64             `json:"part_size"`
	PartBegin     int64             `json:"part_begin"`
	PartEnd       int64             `json:"part_end"`
	FileNum       int               `json:"file_num"`
	FileTotal     int               `json:"file_total"`
	FileSize      int64             `json:"file_size"`
	Subject       string            `json:"subject"`
	Poster        string            `json:"poster"`
	Groups        []string          `json:"groups"`
	MsgId         string            `json:"msg_id"`
	XNxgHeader    string            `json:"x_nxg_header"`
	Filename      string            `json:"filename"`
	CustomHeaders map[string]string `json:"custom_headers"`
	Date          *time.Time        `json:"date"`
	body          []byte
}

func (a *articleData) EncodeBytes() (io.Reader, error) {
	var buf bytes.Buffer

	// Write article headers
	if a.Date == nil {
		fmt.Fprintf(&buf, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123))
	} else {
		fmt.Fprintf(&buf, "Date: %s\r\n", a.Date.UTC().Format(time.RFC1123))
	}

	fmt.Fprintf(&buf, "Subject: %s\r\n", a.Subject)
	fmt.Fprintf(&buf, "From: %s\r\n", a.Poster)
	fmt.Fprintf(&buf, "Newsgroups: %s\r\n", strings.Join(a.Groups, ","))
	fmt.Fprintf(&buf, "Message-ID: <%s>\r\n", a.MsgId)

	if a.XNxgHeader != "" {
		fmt.Fprintf(&buf, "X-Nxg: %s\r\n", a.XNxgHeader)
	}

	for k, v := range a.CustomHeaders {
		fmt.Fprintf(&buf, "%s: %s\r\n", k, v)
	}

	// Blank line separating headers from body
	buf.WriteString("\r\n")

	// yEnc encode body via streaming encoder (handles =ybegin, =ypart, =yend automatically)
	meta := rapidyenc.Meta{
		FileName:   a.Filename,
		FileSize:   a.FileSize,
		PartNumber: a.PartNum,
		TotalParts: a.PartTotal,
		Offset:     a.PartBegin,
		PartSize:   a.PartSize,
	}

	enc, err := rapidyenc.NewEncoder(&buf, meta)
	if err != nil {
		return nil, err
	}

	if _, err := enc.Write(a.body); err != nil {
		return nil, err
	}

	if err := enc.Close(); err != nil {
		return nil, err
	}

	return &buf, nil
}
