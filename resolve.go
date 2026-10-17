package ncbigenome

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// maxPageSize is the page size the CLI uses for report requests.
const maxPageSize = 1000

// Values of the report request's returned_content.
const (
	contentComplete       = "COMPLETE" // full assembly data reports
	contentAccessionsOnly = "ASSM_ACC" // accessions only: all a download needs
)

type sortField struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

// defaultSort is the sort order the CLI always sends with report requests.
var defaultSort = []sortField{
	{"organismName", "SORT_DIRECTION_ASCENDING"},
	{"isRefGenome", "SORT_DIRECTION_DESCENDING"},
	{"isRepGenome", "SORT_DIRECTION_DESCENDING"},
	{"isRefseq", "SORT_DIRECTION_DESCENDING"},
	{"accession", "SORT_DIRECTION_ASCENDING"},
}

// filters is the subset of V2AssemblyDatasetDescriptorsFilter that this library
// sends. The booleans are always sent explicitly (as false when unset), like the
// CLI does; assembly levels are sent only when given, and search text and release
// dates are not supported.
type filters struct {
	ReferenceOnly       bool     `json:"reference_only"`
	AssemblySource      string   `json:"assembly_source"`
	HasAnnotation       bool     `json:"has_annotation"`
	ExcludeAtypical     bool     `json:"exclude_atypical"`
	AssemblyVersion     string   `json:"assembly_version"`
	IsMetagenomeDerived string   `json:"is_metagenome_derived"`
	IsTypeMaterial      bool     `json:"is_type_material"`
	ExcludeMultiIsolate bool     `json:"exclude_multi_isolate"`
	AssemblyLevel       []string `json:"assembly_level,omitempty"`
}

func newFilters(version string, f Filters) (filters, error) {
	out := filters{
		ReferenceOnly:       f.ReferenceOnly,
		AssemblySource:      "all",
		HasAnnotation:       f.AnnotatedOnly,
		ExcludeAtypical:     f.ExcludeAtypical,
		AssemblyVersion:     version,
		IsMetagenomeDerived: magWire[MAGAll],
		IsTypeMaterial:      f.TypeMaterialOnly,
		ExcludeMultiIsolate: f.ExcludeMultiIsolate,
	}
	switch f.AssemblySource {
	case "", SourceAll:
	case SourceRefSeq, SourceGenBank:
		out.AssemblySource = string(f.AssemblySource)
	default:
		return out, fmt.Errorf("unknown assembly source %q", string(f.AssemblySource))
	}
	if f.MAG != "" {
		w, ok := magWire[f.MAG]
		if !ok {
			return out, fmt.Errorf("unknown MAG filter %q", string(f.MAG))
		}
		out.IsMetagenomeDerived = w
	}
	for _, l := range f.AssemblyLevels {
		w, ok := levelWire[l]
		if !ok {
			return out, fmt.Errorf("unknown assembly level %q", string(l))
		}
		out.AssemblyLevel = append(out.AssemblyLevel, w)
	}
	return out, nil
}

// reportsRequest is the body of POST /genome/dataset_report.
type reportsRequest struct {
	Bioprojects     []string    `json:"bioprojects,omitempty"`
	Accessions      []string    `json:"accessions,omitempty"`
	Filters         filters     `json:"filters"`
	ReturnedContent string      `json:"returned_content"`
	PageSize        int         `json:"page_size"`
	PageToken       string      `json:"page_token,omitempty"`
	Sort            []sortField `json:"sort"`
}

type reportsPage struct {
	Reports       []AssemblyDataReport `json:"reports"`
	NextPageToken string               `json:"next_page_token"`
}

// AssemblyReports validates req.Accessions, applies req.AssemblyVersion and
// req.Filters and asks NCBI which assemblies they denote, returning the full
// genome assembly report of each. BioProject accessions expand to their
// assemblies and unversioned accessions to the current version. Only those
// three fields of req are used. The result is de-duplicated by accession and
// may be empty. Use Accessions to reduce it to the accession strings.
func (c *Client) AssemblyReports(ctx context.Context, req Request) ([]AssemblyDataReport, error) {
	set, err := parseAccessions(req.Accessions, req.AssemblyVersion)
	if err != nil {
		return nil, err
	}
	return c.resolveReports(ctx, set, req.Filters, contentComplete)
}

// Accessions returns the assembly accession of each report.
func Accessions(reports []AssemblyDataReport) []string {
	out := make([]string, 0, len(reports))
	for _, r := range reports {
		out = append(out, r.Accession)
	}
	return out
}

// resolve is what downloads use: the same lookup, but asking only for accessions.
func (c *Client) resolve(ctx context.Context, set *accessionSet, f Filters) ([]string, error) {
	reports, err := c.resolveReports(ctx, set, f, contentAccessionsOnly)
	if err != nil {
		return nil, err
	}
	return Accessions(reports), nil
}

func (c *Client) resolveReports(ctx context.Context, set *accessionSet, f Filters, content string) ([]AssemblyDataReport, error) {
	wireFilters, err := newFilters(set.versionFilter, f)
	if err != nil {
		return nil, err
	}
	base := func() reportsRequest {
		return reportsRequest{
			Filters:         wireFilters,
			ReturnedContent: content,
			PageSize:        maxPageSize,
			Sort:            defaultSort,
		}
	}
	var batches []reportsRequest
	if len(set.bioprojects) > 0 {
		r := base()
		r.Bioprojects = set.bioprojects
		batches = append(batches, r)
	}
	for i := 0; i < len(set.assemblies); i += maxPageSize {
		end := i + maxPageSize
		if end > len(set.assemblies) {
			end = len(set.assemblies)
		}
		r := base()
		r.Accessions = set.assemblies[i:end]
		batches = append(batches, r)
	}

	seen := map[string]bool{}
	var out []AssemblyDataReport
	for _, b := range batches {
		reports, err := c.collect(ctx, b)
		if err != nil {
			return nil, err
		}
		for _, r := range reports {
			if r.Accession == "" || seen[r.Accession] {
				continue
			}
			seen[r.Accession] = true
			out = append(out, r)
		}
	}
	return out, nil
}

// collect follows next_page_token until the API reports no more pages.
func (c *Client) collect(ctx context.Context, req reportsRequest) ([]AssemblyDataReport, error) {
	var out []AssemblyDataReport
	prevToken := ""
	for {
		resp, err := c.post(ctx, "/genome/dataset_report", req, "application/json")
		if err != nil {
			return nil, err
		}
		page, err := decodePage(resp)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Reports...)
		if page.NextPageToken == "" {
			return out, nil
		}
		if page.NextPageToken == prevToken {
			return nil, fmt.Errorf("ncbi datasets API: page token did not advance")
		}
		prevToken = page.NextPageToken
		req.PageToken = page.NextPageToken
	}
}

func decodePage(resp *http.Response) (*reportsPage, error) {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp, "")
	}
	var page reportsPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("decoding assembly report page: %w", err)
	}
	return &page, nil
}
