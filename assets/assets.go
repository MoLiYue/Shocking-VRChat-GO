// Package assets embeds the Web UI build, legacy HTML templates and the
// default wave presets.
package assets

import (
	"archive/zip"
	"bytes"
	"embed"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:generate go run ../tools/packassets

// FS holds static/ (Vite build output) and templates/.
//
//go:embed all:static templates
var FS embed.FS

//go:embed bundled.zip
var bundled []byte

// Static returns the SPA build sub-filesystem.
func Static() fs.FS {
	sub, _ := fs.Sub(FS, "static")
	return sub
}

// HasSPA reports whether static/index.html was embedded.
func HasSPA() bool {
	_, err := fs.Stat(FS, "static/index.html")
	return err == nil
}

// Template returns the contents of templates/<name>.
func Template(name string) ([]byte, error) { return FS.ReadFile("templates/" + name) }

// ExtractDefaults writes bundled wave_presets/ and dg-lab/ into baseDir for
// every top-level directory that does not exist yet. User files are never
// overwritten.
func ExtractDefaults(baseDir string) (int, error) {
	zr, err := zip.NewReader(bytes.NewReader(bundled), int64(len(bundled)))
	if err != nil {
		return 0, err
	}
	missing := map[string]bool{}
	for _, d := range []string{"wave_presets", "dg-lab"} {
		if _, err := os.Stat(filepath.Join(baseDir, d)); os.IsNotExist(err) {
			missing[d] = true
		}
	}
	n := 0
	for _, f := range zr.File {
		top := strings.SplitN(f.Name, "/", 2)[0]
		if !missing[top] || strings.Contains(f.Name, "..") {
			continue
		}
		dst := filepath.Join(baseDir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return n, err
		}
		r, err := f.Open()
		if err != nil {
			return n, err
		}
		w, err := os.Create(dst)
		if err != nil {
			r.Close()
			return n, err
		}
		_, err = io.Copy(w, r)
		r.Close()
		w.Close()
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
