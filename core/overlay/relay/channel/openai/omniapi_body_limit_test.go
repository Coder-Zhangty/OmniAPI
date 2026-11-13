package openai

import (
	"bytes"
	"errors"
	"github.com/QuantumNous/new-api/constant"
	"io"
	"strings"
	"testing"
)

type omniBrokenBody struct{}

func (omniBrokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestOmniResilientBodyLimits(t *testing.T) {
	old := constant.MaxRequestBodyMB
	defer func() { constant.MaxRequestBodyMB = old }()
	constant.MaxRequestBodyMB = 32
	for _, size := range []int{16 << 20, (16 << 20) + 1, 17 << 20, 32 << 20} {
		data := bytes.Repeat([]byte(" "), size)
		got, err := omniReadResilientBody(bytes.NewReader(data))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("valid %d-byte body rejected or changed: %v", size, err)
		}
	}
	constant.MaxRequestBodyMB = 1
	if _, err := omniReadResilientBody(bytes.NewReader(make([]byte, (1<<20)+1))); err == nil {
		t.Fatal("configured limit ignored")
	}
	if _, err := omniReadResilientBody(omniBrokenBody{}); !errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("read error misclassified: %v", err)
	}
	constant.MaxRequestBodyMB = 0
	if _, err := omniReadResilientBody(strings.NewReader("{}")); err != nil {
		t.Fatal(err)
	}
}
