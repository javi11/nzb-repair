package repairnzb

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"time"
)

type Encoder interface {
	Encode(p []byte) []byte
}

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

func (a *articleData) EncodeBytes(encoder Encoder) (io.Reader, error) {
	headers := make(map[string]string)

	if a.CustomHeaders != nil {
		for k, v := range a.CustomHeaders {
			headers[k] = v
		}
	}

	headers["Subject"] = a.Subject
	headers["From"] = a.Poster
	headers["Newsgroups"] = strings.Join(a.Groups, ",")
	headers["Message-ID"] = fmt.Sprintf("<%s>", a.MsgId)

	if a.XNxgHeader != "" {
		headers["X-Nxg"] = a.XNxgHeader
	}

	if a.Date == nil {
		headers["Date"] = time.Now().UTC().Format(time.RFC1123)
	} else {
		headers["Date"] = a.Date.UTC().Format(time.RFC1123)
	}

	header := ""
	for k, v := range headers {
		header += fmt.Sprintf("%s: %s\r\n", k, v)
	}

	header += fmt.Sprintf("\r\n=ybegin part=%d total=%d line=128 size=%d name=%s\r\n=ypart begin=%d end=%d\r\n",
		a.PartNum, a.PartTotal, a.FileSize, a.Filename, a.PartBegin+1, a.PartEnd)

	// Encoded data
	encoded := encoder.Encode(a.body)

	// yEnc end line
	h := crc32.NewIEEE()
	_, err := h.Write(a.body)
	if err != nil {
		return nil, err
	}
	footer := fmt.Sprintf("\r\n=yend size=%d part=%d pcrc32=%08X\r\n", a.PartSize, a.PartNum, h.Sum32())

	size := len(header) + len(encoded) + len(footer)
	buf := bytes.NewBuffer(make([]byte, 0, size))

	_, err = buf.WriteString(header)
	if err != nil {
		return nil, err
	}
	_, err = buf.Write(encoded)
	if err != nil {
		return nil, err
	}
	_, err = buf.WriteString(footer)
	if err != nil {
		return nil, err
	}

	return buf, nil
}
