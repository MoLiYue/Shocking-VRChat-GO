// Command packassets bundles wave_presets/ and dg-lab/ into
// assets/bundled.zip so they can be embedded into the executable.
//
// The files cannot be embedded directly: their names contain characters
// (emoji, full-width punctuation) that go:embed rejects.
package main

import (
	"archive/zip"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
)

func main() {
	out := filepath.Join("assets", "bundled.zip")
	f, err := os.Create(out)
	if err != nil {
		log.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, dir := range []string{"wave_presets", "dg-lab"} {
		var files []string
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				files = append(files, p)
			}
			return nil
		})
		sort.Strings(files)
		for _, p := range files {
			w, err := zw.CreateHeader(&zip.FileHeader{Name: filepath.ToSlash(p), Method: zip.Deflate})
			if err != nil {
				log.Fatal(err)
			}
			r, err := os.Open(p)
			if err != nil {
				log.Fatal(err)
			}
			if _, err := io.Copy(w, r); err != nil {
				log.Fatal(err)
			}
			r.Close()
		}
	}
	if err := zw.Close(); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %s", out)
}
