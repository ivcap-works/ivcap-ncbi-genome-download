package ncbigenome

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoAssemblies is returned when the accessions resolve to nothing.
var ErrNoAssemblies = errors.New("no genome assemblies match the request")

// ErrNotSingleGenome is returned by Download and DownloadToFile when the request
// does not denote exactly one genome assembly. Test for it with errors.Is.
var ErrNotSingleGenome = errors.New("a download needs exactly one genome assembly")

// downloadBody is the body of POST /genome/download (V2AssemblyDatasetRequest).
// include_annotation_type has no omitempty on purpose: for IncludeNone the CLI
// sends an explicit empty list, and the API distinguishes that from "absent".
type downloadBody struct {
	Accessions            []string `json:"accessions"`
	Chromosomes           []string `json:"chromosomes,omitempty"`
	IncludeAnnotationType []string `json:"include_annotation_type"`
	Hydrated              string   `json:"hydrated,omitempty"`
}

// buildIncludes validates the include list and maps it to wire values, like the
// CLI's SetRequestArgs. The returned slice is never nil.
func buildIncludes(include []Include, chromosomes []string) (wire []string, warning string, err error) {
	if len(include) == 0 {
		include = []Include{IncludeGenome}
	}
	for _, inc := range include {
		if inc == IncludeAll && len(include) != 1 {
			return nil, "", errors.New("if file type 'all' is requested, no other file types may be included")
		}
		if inc == IncludeNone && len(include) != 1 {
			return nil, "", errors.New("if file type 'none' is requested, no other file types may be included")
		}
	}
	if len(include) == 1 && include[0] == IncludeAll {
		include = allIncludes
	}
	wire = []string{}
	seen := map[Include]bool{}
	for _, inc := range include {
		if inc == IncludeNone {
			break
		}
		w, ok := includeWire[inc]
		if !ok {
			return nil, "", fmt.Errorf("unknown include value %q", string(inc))
		}
		if seen[inc] {
			continue
		}
		seen[inc] = true
		wire = append(wire, w)
		if len(chromosomes) > 0 && warning == "" && inc != IncludeGenome && inc != IncludeSeqReport {
			warning = "included annotation files cover the whole genome even though a chromosome FASTA was selected"
		}
	}
	return wire, warning, nil
}

type prepared struct {
	body    downloadBody
	warning string
}

// prepare validates the request without touching the network except for the
// accession resolution call.
func (c *Client) prepare(ctx context.Context, req Request) (*prepared, error) {
	include, warning, err := buildIncludes(req.Include, req.Chromosomes)
	if err != nil {
		return nil, err
	}
	set, err := parseAccessions(req.Accessions, req.AssemblyVersion)
	if err != nil {
		return nil, err
	}
	if err := requireSingleAssemblyAccession(set); err != nil {
		return nil, err
	}
	accs, err := c.resolve(ctx, set, req.Filters)
	if err != nil {
		return nil, err
	}
	if len(accs) == 0 {
		return nil, ErrNoAssemblies
	}
	if len(accs) != 1 {
		return nil, notSingleGenome(accs)
	}
	body := downloadBody{Accessions: accs, IncludeAnnotationType: include}
	if len(req.Chromosomes) > 0 {
		body.Chromosomes = req.Chromosomes
	}
	if req.Dehydrated {
		body.Hydrated = "DATA_REPORT_ONLY"
	}
	return &prepared{body: body, warning: warning}, nil
}

// Download streams the package zip of exactly one genome to w; see
// ErrNotSingleGenome. It does not validate the archive; use DownloadToFile for
// that. With Request.Manifest it also makes a temporary copy of the zip to read
// the file list from; see Request.Manifest.
func (c *Client) Download(ctx context.Context, req Request, w io.Writer) (*Result, error) {
	p, err := c.prepare(ctx, req)
	if err != nil {
		return nil, err
	}

	var spool *os.File
	dst := w
	if req.Manifest {
		spool, err = os.CreateTemp(c.tempDir, "ncbigenome-manifest-*.zip")
		if err != nil {
			return nil, fmt.Errorf("creating a temporary file for the manifest: %w", err)
		}
		defer func() {
			spool.Close()
			os.Remove(spool.Name())
		}()
		dst = io.MultiWriter(w, spool)
	}

	n, err := c.fetch(ctx, p, dst, req.OnProgress)
	if err != nil {
		return nil, err
	}
	res := &Result{Accession: p.body.Accessions[0], Bytes: n, Warning: p.warning}
	if spool != nil {
		if err := spool.Close(); err != nil {
			return nil, err
		}
		res.Manifest, err = ReadManifest(spool.Name())
		if err != nil {
			return nil, fmt.Errorf("reading the package manifest: %w", err)
		}
	}
	return res, nil
}

// DownloadToFile downloads the package of exactly one genome (see
// ErrNotSingleGenome) to path. The data is written to a temporary file next to
// path, checked with ValidateZip and then renamed, so path never holds a partial
// or corrupt download.
func (c *Client) DownloadToFile(ctx context.Context, req Request, path string) (*Result, error) {
	p, err := c.prepare(ctx, req)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".part-*")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	n, err := c.fetch(ctx, p, tmp, req.OnProgress)
	if err != nil {
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := ValidateZip(tmpName, !req.SkipZipContentCheck); err != nil {
		return nil, fmt.Errorf("downloaded package is not a valid zip archive: %w", err)
	}
	manifest, err := ReadManifest(tmpName)
	if err != nil {
		return nil, fmt.Errorf("reading the package manifest: %w", err)
	}
	_ = os.Chmod(tmpName, 0o644)
	if err := os.Rename(tmpName, path); err != nil {
		return nil, err
	}
	done = true
	return &Result{Accession: p.body.Accessions[0], Bytes: n, Path: path, Warning: p.warning, Manifest: manifest}, nil
}

// fetch performs POST /genome/download and copies the zip stream to w.
func (c *Client) fetch(ctx context.Context, p *prepared, w io.Writer, progress func(int64)) (int64, error) {
	resp, err := c.post(ctx, "/genome/download", p.body, "text/plain, application/zip")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return 0, apiError(resp, "request does not match any items in the NCBI database")
	case http.StatusTooManyRequests:
		return 0, apiError(resp, "retries exhausted; the selection may be too large for direct download - try Dehydrated, or retry later if rate limited")
	default:
		return 0, apiError(resp, "")
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return 0, apiError(resp, "expected a zip archive but received JSON")
	}

	dst := w
	if progress != nil {
		dst = &progressWriter{w: w, fn: progress}
	}
	return io.Copy(dst, resp.Body)
}

type progressWriter struct {
	w  io.Writer
	fn func(int64)
	n  int64
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n += int64(n)
	p.fn(p.n)
	return n, err
}

// requireSingleAssemblyAccession rejects input that cannot denote exactly one
// genome: BioProject accessions (a project can name many assemblies) and any
// number of assembly accessions other than one. Repeating one accession is fine.
func requireSingleAssemblyAccession(set *accessionSet) error {
	if len(set.bioprojects) > 0 {
		return fmt.Errorf("%w: %s is a BioProject accession and can name many genomes; "+
			"use AssemblyReports to see which assemblies it has, then pass one assembly accession",
			ErrNotSingleGenome, strings.Join(set.bioprojects, ", "))
	}
	if len(set.assemblies) != 1 {
		return fmt.Errorf("%w: got %d assembly accessions, want exactly one", ErrNotSingleGenome, len(set.assemblies))
	}
	return nil
}

// notSingleGenome describes a lookup that matched several assemblies, listing at
// most five of them.
func notSingleGenome(accs []string) error {
	shown, more := accs, ""
	if len(accs) > 5 {
		shown = accs[:5]
		more = fmt.Sprintf(" and %d more", len(accs)-5)
	}
	return fmt.Errorf("%w: the request matches %d assemblies (%s%s); name one specific assembly accession, with its version",
		ErrNotSingleGenome, len(accs), strings.Join(shown, ", "), more)
}
