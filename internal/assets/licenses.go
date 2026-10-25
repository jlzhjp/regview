package assets

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
)

// WriteLicenses prints the third-party license notices embedded in this build.
// Go source files collected for redistribution remain available in the archive.
func WriteLicenses(w io.Writer) error {
	files, err := readLicenseArchive()
	if err != nil {
		return err
	}
	type notice struct {
		name string
		text []byte
	}
	var notices []notice
	for name, data := range files {
		if displayName, ok := strings.CutPrefix(name, "go/"); ok {
			notices = append(notices, notice{displayName, data})
		}
	}
	index, ok := files["npm/index.json"]
	if !ok {
		return errors.New("license archive is missing npm/index.json")
	}
	var packages []struct {
		Name    string   `json:"name"`
		Version string   `json:"version"`
		License string   `json:"license"`
		Files   []string `json:"files"`
	}
	if err := json.Unmarshal(index, &packages); err != nil {
		return fmt.Errorf("parse npm license index: %w", err)
	}
	for _, pkg := range packages {
		id := pkg.Name + "@" + pkg.Version
		for _, file := range pkg.Files {
			data, ok := files[path.Join("npm", id, file)]
			if !ok {
				return fmt.Errorf("license archive is missing npm/%s/%s", id, file)
			}
			notices = append(notices, notice{id + "/" + file + " (" + pkg.License + ")", data})
		}
		if len(pkg.Files) == 0 {
			notices = append(notices, notice{id + " (" + pkg.License + ")", []byte("No license notice file was included in this package.\n")})
		}
	}
	slices.SortFunc(notices, func(a, b notice) int { return strings.Compare(a.name, b.name) })
	var output bytes.Buffer
	output.WriteString("Third-party licenses for regview\n")
	for _, entry := range notices {
		fmt.Fprintf(&output, "\n--- %s ---\n\n%s\n", entry.name, entry.text)
	}
	_, err = output.WriteTo(w)
	return err
}

func readLicenseArchive() (files map[string][]byte, err error) {
	const filename = "licenses.tar.gz"
	archive, err := embeddedAssets.ReadFile("dist/" + filename)
	if err != nil {
		return nil, fmt.Errorf("read %s (run just generate): %w", filename, err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", filename, err)
	}
	defer func() { err = errors.Join(err, gz.Close()) }()
	reader := tar.NewReader(gz)
	files = make(map[string][]byte)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filename, err)
		}
		name := strings.TrimPrefix(header.Name, "./")
		// Keep required Go source in the download, but print only its notices.
		include := strings.HasPrefix(name, "npm/") || (strings.HasPrefix(name, "go/") && isLicenseNotice(name))
		if header.Typeflag != tar.TypeReg || !include {
			continue
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return nil, fmt.Errorf("read %s from %s: %w", header.Name, filename, err)
		}
		files[name] = data
	}
}

func isLicenseNotice(name string) bool {
	base := strings.ToUpper(path.Base(name))
	for _, prefix := range []string{"LICENSE", "LICENCE", "COPYING", "NOTICE", "COPYRIGHT"} {
		if base == prefix || strings.HasPrefix(base, prefix+".") || strings.HasPrefix(base, prefix+"-") || strings.HasPrefix(base, prefix+"_") {
			return true
		}
	}
	return false
}
