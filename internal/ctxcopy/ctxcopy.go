// Package ctxcopy provides an io.Copy that checks a context between chunks.
package ctxcopy

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// Copy copies from src to dst and aborts as soon as ctx is done.
func Copy(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 128*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		read, readErr := src.Read(buffer)
		if read > 0 {
			count, writeErr := dst.Write(buffer[:read])
			written += int64(count)
			if writeErr != nil {
				return written, fmt.Errorf("write: %w", writeErr)
			}
			if count != read {
				return written, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, fmt.Errorf("read: %w", readErr)
		}
	}
}
