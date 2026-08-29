package notify

import (
	_ "embed"
	"encoding/base64"
)

//go:embed logo-bqckup.png
var logoPNG []byte

// logoDataURI holds the base64 data URI of the official Bqckup logo.
var logoDataURI = "data:image/png;base64," + base64.StdEncoding.EncodeToString(logoPNG)
