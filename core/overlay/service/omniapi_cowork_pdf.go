package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var omniPDFSlots = make(chan struct{}, 1)
var omniPDFRender = omniRenderPDFPages

func omniRenderPDFPages(data string) ([]any, error) {
	if len(data) > 28<<20 {
		return nil, errors.New("PDF exceeds 20 MiB conversion limit")
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil || !strings.HasPrefix(string(raw), "%PDF-") {
		return nil, errors.New("invalid base64 PDF")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	select {
	case omniPDFSlots <- struct{}{}:
		defer func() { <-omniPDFSlots }()
	case <-ctx.Done():
		return nil, errors.New("PDF conversion busy; please retry")
	}
	dir, err := os.MkdirTemp("", "omni-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	name := filepath.Join(dir, "input.pdf")
	if err = os.WriteFile(name, raw, 0600); err != nil {
		return nil, err
	}
	info, err := exec.CommandContext(ctx, "pdfinfo", name).Output()
	if err != nil {
		return nil, errors.New("cannot read PDF; encrypted or invalid document")
	}
	pages := 0
	for _, line := range strings.Split(string(info), "\n") {
		if strings.HasPrefix(line, "Pages:") {
			pages, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Pages:")))
		}
	}
	if pages < 1 || pages > 100 {
		return nil, errors.New("PDF must contain 1 to 100 pages per attachment")
	}
	if err = exec.CommandContext(ctx, "pdftoppm", "-png", "-r", "110", "-scale-to", "1600", "-f", "1", "-l", strconv.Itoa(pages), name, filepath.Join(dir, "page")).Run(); err != nil {
		return nil, errors.New("PDF page conversion failed or timed out")
	}
	files, err := filepath.Glob(filepath.Join(dir, "page-*.png"))
	if err != nil || len(files) != pages {
		return nil, errors.New("PDF page conversion incomplete")
	}
	out := []any{map[string]any{"type": "text", "text": fmt.Sprintf("The following %d images are all PDF pages in original page order.", pages)}}
	total := 0
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		total += len(b)
		if total > 20<<20 {
			return nil, errors.New("rendered PDF exceeds image size limit")
		}
		out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(b)}})
	}
	return out, nil
}
func omniConvertPDFValue(v any) (any, error) {
	switch x := v.(type) {
	case []any:
		out := []any{}
		for _, y := range x {
			if part, ok := y.(map[string]any); ok && part["type"] == "document" {
				src, _ := part["source"].(map[string]any)
				if src["type"] == "base64" && src["media_type"] == "application/pdf" {
					data, _ := src["data"].(string)
					pages, err := omniPDFRender(data)
					if err != nil {
						return nil, err
					}
					out = append(out, pages...)
					continue
				}
			}
			converted, err := omniConvertPDFValue(y)
			if err != nil {
				return nil, err
			}
			out = append(out, converted)
		}
		return out, nil
	case map[string]any:
		out := map[string]any{}
		for k, y := range x {
			if k == "content" {
				converted, err := omniConvertPDFValue(y)
				if err != nil {
					return nil, err
				}
				out[k] = converted
			} else {
				out[k] = y
			}
		}
		return out, nil
	}
	return v, nil
}
func omniNormalizeCoworkPDF(r *dto.ClaudeRequest) error {
	replacements := map[int]any{}
	for i, m := range r.Messages {
		b, err := json.Marshal(m.Content)
		if err != nil {
			return err
		}
		var value any
		if err = json.Unmarshal(b, &value); err != nil {
			return err
		}
		converted, err := omniConvertPDFValue(value)
		if err != nil {
			return err
		}
		replacements[i] = converted
	}
	for i, v := range replacements {
		r.Messages[i].Content = v
	}
	return nil
}
