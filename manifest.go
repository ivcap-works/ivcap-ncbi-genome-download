package ncbigenome

import "archive/zip"

// ManifestFile describes one file inside a downloaded package.
type ManifestFile struct {
	// Name is the path inside the zip, e.g. "ncbi_dataset/data/GCF_000001735.4/genomic.gff".
	Name string `json:"name"`
	// Size is the uncompressed size in bytes.
	Size uint64 `json:"size"`
	// CompressedSize is the size in bytes as stored in the zip.
	CompressedSize uint64 `json:"compressed_size"`
	// CRC32 is the CRC-32 (IEEE) checksum of the uncompressed data, as recorded in the zip.
	CRC32 uint32 `json:"crc32"`
}

// Manifest lists the files in a downloaded package, in the order the zip stores
// them. Directory entries are not included.
type Manifest struct {
	Files []ManifestFile `json:"files"`
}

// TotalSize is the sum of the uncompressed sizes of all files. It is 0 for a nil
// manifest.
func (m *Manifest) TotalSize() uint64 {
	if m == nil {
		return 0
	}
	var total uint64
	for _, f := range m.Files {
		total += f.Size
	}
	return total
}

// TotalCompressedSize is the sum of the stored sizes of all files. It is 0 for a
// nil manifest.
func (m *Manifest) TotalCompressedSize() uint64 {
	if m == nil {
		return 0
	}
	var total uint64
	for _, f := range m.Files {
		total += f.CompressedSize
	}
	return total
}

// ReadManifest lists the files of the zip archive at path, reading only the
// archive's central directory, so it is fast even for large packages.
func ReadManifest(path string) (*Manifest, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	m := &Manifest{Files: make([]ManifestFile, 0, len(zr.File))}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		m.Files = append(m.Files, ManifestFile{
			Name:           f.Name,
			Size:           f.UncompressedSize64,
			CompressedSize: f.CompressedSize64,
			CRC32:          f.CRC32,
		})
	}
	return m, nil
}
