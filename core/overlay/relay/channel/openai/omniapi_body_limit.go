package openai

import (
	"fmt"
	"github.com/QuantumNous/new-api/constant"
	"io"
)

func omniReadResilientBody(body io.Reader) ([]byte, error) {
	maxMB := constant.MaxRequestBodyMB
	if maxMB <= 0 {
		maxMB = 128
	}
	limit := int64(maxMB) << 20
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read resilient request body: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("resilient request body exceeds configured %d MiB limit", maxMB)
	}
	return data, nil
}
