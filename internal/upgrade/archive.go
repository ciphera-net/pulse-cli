package upgrade

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// maxBinaryBytes caps what is read out of an archive. Nothing gets here before
// its signature has been checked, so a decompression bomb would have to be one
// we signed ourselves — but the cap costs nothing and removes the question.
const maxBinaryBytes = 256 << 20

// BinaryName is what the executable is called inside the published archive.
func BinaryName(goos string) string {
	if goos == "windows" {
		return "pulse.exe"
	}
	return "pulse"
}

// ExtractBinary pulls the pulse executable out of a release archive.
//
// The format follows the asset name because that is what .goreleaser.yaml keys
// it on: tar.gz everywhere, zip on Windows.
//
// Only the entry whose base name is the binary is read, and only as bytes — no
// path from the archive is ever used to open a file, so a crafted entry name
// like ../../.ssh/authorized_keys has nothing to act on. Extraction never
// touches the filesystem at all.
func ExtractBinary(assetName string, data []byte, binName string) ([]byte, error) {
	switch {
	case strings.HasSuffix(assetName, ".tar.gz"), strings.HasSuffix(assetName, ".tgz"):
		return fromTarGz(data, binName)
	case strings.HasSuffix(assetName, ".zip"):
		return fromZip(data, binName)
	default:
		return nil, fmt.Errorf("%s is not an archive format this build can open", assetName)
	}
}

func fromTarGz(data []byte, binName string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("the downloaded archive is not gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading the downloaded archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || path.Base(hdr.Name) != binName {
			continue
		}
		return readCapped(tr, binName)
	}
	return nil, fmt.Errorf("the downloaded archive contains no %s", binName)
}

func fromZip(data []byte, binName string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("the downloaded archive is not a zip: %w", err)
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || path.Base(f.Name) != binName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("reading %s from the downloaded archive: %w", binName, err)
		}
		defer rc.Close()
		return readCapped(rc, binName)
	}
	return nil, fmt.Errorf("the downloaded archive contains no %s", binName)
}

func readCapped(r io.Reader, binName string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxBinaryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s from the downloaded archive: %w", binName, err)
	}
	if len(b) > maxBinaryBytes {
		return nil, fmt.Errorf("%s in the downloaded archive is implausibly large", binName)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("%s in the downloaded archive is empty", binName)
	}
	return b, nil
}
