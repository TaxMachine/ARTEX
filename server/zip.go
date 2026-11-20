package server

import (
	"archive/zip"
	"io"
	"net/http"
)

// writeZip streams map[name]content as a zip archive.
func writeZip(w http.ResponseWriter, files map[string]string) {
	zw := zip.NewWriter(w)
	for name, content := range files {
		fw, err := zw.Create(name)
		if err != nil {
			continue
		}
		_, _ = io.WriteString(fw, content)
	}
	_ = zw.Close()
}
