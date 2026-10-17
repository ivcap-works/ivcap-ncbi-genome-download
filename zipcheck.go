package ncbigenome

import (
	"archive/zip"
	"fmt"
	"io"
)

// ValidateZip checks that path is a readable zip archive. With checkContents it
// also reads every entry to the end, which makes Go verify each entry's CRC;
// without it only the zip directory is checked and entries are opened but not
// read (the CLI's --skip-zip-validation behaviour).
func ValidateZip(path string, checkContents bool) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
		if checkContents {
			_, err = io.Copy(io.Discard, rc)
		}
		rc.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	return nil
}
