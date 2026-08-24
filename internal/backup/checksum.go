package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// ChecksumFile returns the hex SHA-256 and the byte size of the file at path.
func ChecksumFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return "", 0, fmt.Errorf("checksum: %w", copyErr)
	}
	if closeErr != nil {
		return "", 0, fmt.Errorf("close: %w", closeErr)
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}
