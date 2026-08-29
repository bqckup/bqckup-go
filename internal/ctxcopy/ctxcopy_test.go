package ctxcopy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCopyCopiesAndHonorsContext(t *testing.T) {
	var out strings.Builder
	n, err := Copy(context.Background(), &out, strings.NewReader("hello"))
	if err != nil || n != 5 || out.String() != "hello" {
		t.Fatalf("Copy = (%d, %v, %q), want (5, nil, %q)", n, err, out.String(), "hello")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, err = Copy(ctx, &out, strings.NewReader("hello"))
	if !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatalf("Copy with cancelled ctx = (%d, %v), want (0, context.Canceled)", n, err)
	}
}
