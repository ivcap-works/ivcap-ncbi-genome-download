package ncbigenome

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func makeZip(t *testing.T, method uint16, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseAccessionsVersionPolicy(t *testing.T) {
	tests := []struct {
		name       string
		in         []string
		version    AssemblyVersion
		wantAsm    []string
		wantBio    []string
		wantFilter string
	}{
		{"versioned default keeps version", []string{"gcf_000001735.4"}, VersionDefault, []string{"GCF_000001735.4"}, nil, "all_assemblies"},
		{"unversioned default is current", []string{"GCF_000001735"}, VersionDefault, []string{"GCF_000001735"}, nil, "current"},
		{"versioned with current strips", []string{"GCF_000001735.4"}, VersionCurrent, []string{"GCF_000001735"}, nil, "current"},
		{"versioned with all strips", []string{"GCF_000001735.4"}, VersionAll, []string{"GCF_000001735"}, nil, "all_assemblies"},
		{"dedupe after strip, bioproject split", []string{"PRJNA289059", "GCF_1.1", "GCF_1.2"}, VersionCurrent, []string{"GCF_1"}, []string{"PRJNA289059"}, "current"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			set, err := parseAccessions(tc.in, tc.version)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(set.assemblies, tc.wantAsm) {
				t.Errorf("assemblies = %v, want %v", set.assemblies, tc.wantAsm)
			}
			if !reflect.DeepEqual(set.bioprojects, tc.wantBio) {
				t.Errorf("bioprojects = %v, want %v", set.bioprojects, tc.wantBio)
			}
			if set.versionFilter != tc.wantFilter {
				t.Errorf("filter = %q, want %q", set.versionFilter, tc.wantFilter)
			}
		})
	}
}

func TestParseAccessionsRejectsBadInput(t *testing.T) {
	for _, bad := range []string{"XYZ_123", "GCF_", "GCF_12x", "PRJXX123", "GCF_000001735.4; DROP"} {
		if _, err := parseAccessions([]string{bad}, VersionDefault); err == nil {
			t.Errorf("accession %q was accepted", bad)
		}
	}
	if _, err := parseAccessions(nil, VersionDefault); err == nil {
		t.Error("empty accession list was accepted")
	}
}

func TestBuildIncludes(t *testing.T) {
	wire, warn, err := buildIncludes(nil, nil)
	if err != nil || !reflect.DeepEqual(wire, []string{"GENOME_FASTA"}) || warn != "" {
		t.Fatalf("default: %v %q %v", wire, warn, err)
	}

	wire, _, err = buildIncludes([]Include{IncludeAll}, nil)
	want := []string{"GENOME_FASTA", "PROT_FASTA", "CDS_FASTA", "GENOME_GFF", "GENOME_GTF", "GENOME_GBFF", "RNA_FASTA", "SEQUENCE_REPORT"}
	if err != nil || !reflect.DeepEqual(wire, want) {
		t.Fatalf("all: %v %v", wire, err)
	}

	wire, _, err = buildIncludes([]Include{IncludeNone}, nil)
	if err != nil || wire == nil || len(wire) != 0 {
		t.Fatalf("none must be a non-nil empty slice, got %#v (%v)", wire, err)
	}

	if _, _, err = buildIncludes([]Include{IncludeAll, IncludeGFF3}, nil); err == nil {
		t.Error("all + gff3 should be rejected")
	}
	if _, _, err = buildIncludes([]Include{IncludeNone, IncludeGenome}, nil); err == nil {
		t.Error("none + genome should be rejected")
	}
	if _, _, err = buildIncludes([]Include{"bogus"}, nil); err == nil {
		t.Error("unknown include should be rejected")
	}

	_, warn, err = buildIncludes([]Include{IncludeGenome, IncludeGFF3}, []string{"1"})
	if err != nil || warn == "" {
		t.Errorf("chromosome + gff3 should warn, got %q (%v)", warn, err)
	}
	_, warn, _ = buildIncludes([]Include{IncludeGenome, IncludeSeqReport}, []string{"1"})
	if warn != "" {
		t.Errorf("chromosome + genome/seq-report should not warn, got %q", warn)
	}
}

func TestParseIncludes(t *testing.T) {
	got, err := ParseIncludes("Genome, GFF3 ,seq-report")
	if err != nil || !reflect.DeepEqual(got, []Include{IncludeGenome, IncludeGFF3, IncludeSeqReport}) {
		t.Fatalf("got %v (%v)", got, err)
	}
	if got, err := ParseIncludes(""); err != nil || got != nil {
		t.Fatalf("empty: %v (%v)", got, err)
	}
	if _, err := ParseIncludes("genome,nope"); err == nil {
		t.Error("unknown name accepted")
	}
}

func TestDownloadBodyJSON(t *testing.T) {
	b, err := json.Marshal(downloadBody{Accessions: []string{"GCF_000001735.4"}, IncludeAnnotationType: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"include_annotation_type":[]`) {
		t.Errorf("empty include list must be sent as [], got %s", s)
	}
	if strings.Contains(s, "chromosomes") || strings.Contains(s, "hydrated") {
		t.Errorf("unset fields must be omitted, got %s", s)
	}
}

func TestDownloadToFile(t *testing.T) {
	zipBytes := makeZip(t, zip.Deflate, map[string]string{
		"ncbi_dataset/data/assembly_data_report.jsonl": "{}\n",
		"ncbi_dataset/dataset_catalog.json":            "{}",
	})
	var mu sync.Mutex
	var reportReq, dlReq map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/genome/dataset_report":
			mu.Lock()
			_ = json.Unmarshal(body, &reportReq)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"reports":[{"accession":"GCF_000001735.4"}]}`)
		case "/genome/download":
			if r.Header.Get("api-key") != "k" {
				http.Error(w, "missing key", http.StatusUnauthorized)
				return
			}
			mu.Lock()
			_ = json.Unmarshal(body, &dlReq)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/zip")
			w.Write(zipBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var last int64
	c := New(Options{BaseURL: srv.URL, APIKey: "k"})
	out := filepath.Join(t.TempDir(), "pkg.zip")
	res, err := c.DownloadToFile(context.Background(), Request{
		Accessions: []string{"GCF_000001735.4"},
		Include:    []Include{IncludeGenome, IncludeGFF3},
		OnProgress: func(n int64) { last = n },
	}, out)
	if err != nil {
		t.Fatal(err)
	}
	if res.Accession != "GCF_000001735.4" || res.Bytes != int64(len(zipBytes)) || res.Path != out {
		t.Errorf("unexpected result %+v", res)
	}
	if last != int64(len(zipBytes)) {
		t.Errorf("progress ended at %d, want %d", last, len(zipBytes))
	}
	if err := ValidateZip(out, true); err != nil {
		t.Errorf("output is not a valid zip: %v", err)
	}
	checkManifest(t, res.Manifest, map[string]string{
		"ncbi_dataset/data/assembly_data_report.jsonl": "{}\n",
		"ncbi_dataset/dataset_catalog.json":            "{}",
	})
	entries, _ := os.ReadDir(filepath.Dir(out))
	if len(entries) != 1 {
		t.Errorf("expected only the final file, found %d entries", len(entries))
	}

	mu.Lock()
	defer mu.Unlock()
	f, _ := reportReq["filters"].(map[string]any)
	if f["assembly_version"] != "all_assemblies" || f["assembly_source"] != "all" ||
		f["is_metagenome_derived"] != "METAGENOME_DERIVED_UNSET" || f["reference_only"] != false {
		t.Errorf("unexpected report filters: %v", f)
	}
	if reportReq["returned_content"] != "ASSM_ACC" || reportReq["page_size"] != float64(1000) {
		t.Errorf("unexpected report request: %v", reportReq)
	}
	if !reflect.DeepEqual(dlReq["include_annotation_type"], []any{"GENOME_FASTA", "GENOME_GFF"}) ||
		!reflect.DeepEqual(dlReq["accessions"], []any{"GCF_000001735.4"}) {
		t.Errorf("unexpected download request: %v", dlReq)
	}
}

func TestResolveFollowsPageTokens(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		atomic.AddInt32(&calls, 1)
		if req["page_token"] == "t2" {
			io.WriteString(w, `{"reports":[{"accession":"GCA_2.1"}]}`)
			return
		}
		io.WriteString(w, `{"reports":[{"accession":"GCA_1.1"}],"next_page_token":"t2"}`)
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL})
	got, err := reportAccessions(c, Request{Accessions: []string{"PRJNA1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"GCA_1.1", "GCA_2.1"}) || atomic.LoadInt32(&calls) != 2 {
		t.Errorf("got %v after %d calls", got, calls)
	}
}

func TestRetriesOn503(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"reports":[{"accession":"GCF_000001735.4"}]}`)
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, RetryWaitMin: time.Millisecond, RetryWaitMax: 5 * time.Millisecond})
	got, err := reportAccessions(c, Request{Accessions: []string{"GCF_000001735.4"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"GCF_000001735.4"}) || atomic.LoadInt32(&calls) != 3 {
		t.Errorf("got %v after %d calls", got, calls)
	}
}

func TestDownload429BecomesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/genome/dataset_report" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"reports":[{"accession":"GCF_000001735.4"}]}`)
			return
		}
		http.Error(w, "too big", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, MaxRetries: 1, RetryWaitMin: time.Millisecond, RetryWaitMax: time.Millisecond})
	_, err := c.Download(context.Background(), Request{Accessions: []string{"GCF_000001735.4"}}, io.Discard)
	var ae *APIError
	if !errors.As(err, &ae) || ae.StatusCode != http.StatusTooManyRequests || ae.Hint == "" {
		t.Fatalf("want *APIError with status 429 and a hint, got %v", err)
	}
}

func TestNoAssemblies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL})
	_, err := c.Download(context.Background(), Request{Accessions: []string{"GCF_000001735.4"}}, io.Discard)
	if !errors.Is(err, ErrNoAssemblies) {
		t.Fatalf("want ErrNoAssemblies, got %v", err)
	}
}

func TestValidateZip(t *testing.T) {
	dir := t.TempDir()

	bad := filepath.Join(dir, "bad.zip")
	if err := os.WriteFile(bad, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateZip(bad, false); err == nil {
		t.Error("garbage accepted")
	}

	// A stored (uncompressed) entry with one flipped content byte: the zip
	// directory is fine, but the CRC no longer matches.
	data := makeZip(t, zip.Store, map[string]string{"a.txt": "hello world"})
	i := bytes.Index(data, []byte("hello world"))
	if i < 0 {
		t.Fatal("content not found in zip bytes")
	}
	data[i] = 'J'
	corrupt := filepath.Join(dir, "corrupt.zip")
	if err := os.WriteFile(corrupt, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateZip(corrupt, false); err != nil {
		t.Errorf("shallow check should accept a CRC-corrupt entry: %v", err)
	}
	if err := ValidateZip(corrupt, true); err == nil {
		t.Error("deep check should reject a CRC-corrupt entry")
	}
}

func TestFiltersWire(t *testing.T) {
	f, err := newFilters("current", Filters{
		ReferenceOnly:       true,
		AnnotatedOnly:       true,
		AssemblySource:      SourceRefSeq,
		AssemblyLevels:      []AssemblyLevel{LevelComplete, LevelChromosome},
		ExcludeAtypical:     true,
		ExcludeMultiIsolate: true,
		TypeMaterialOnly:    true,
		MAG:                 MAGExclude,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(f)
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	want := map[string]any{
		"reference_only":        true,
		"has_annotation":        true,
		"assembly_source":       "refseq",
		"assembly_level":        []any{"complete_genome", "chromosome"},
		"exclude_atypical":      true,
		"exclude_multi_isolate": true,
		"is_type_material":      true,
		"is_metagenome_derived": "metagenome_derived_exclude",
		"assembly_version":      "current",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("filters wire form\n got  %v\n want %v", got, want)
	}

	// The zero value reproduces the CLI defaults: everything explicit and off,
	// no assembly_level.
	z, err := newFilters("current", Filters{})
	if err != nil {
		t.Fatal(err)
	}
	zb, _ := json.Marshal(z)
	if strings.Contains(string(zb), "assembly_level") ||
		!strings.Contains(string(zb), `"assembly_source":"all"`) ||
		!strings.Contains(string(zb), `"is_metagenome_derived":"METAGENOME_DERIVED_UNSET"`) {
		t.Errorf("unexpected default filters: %s", zb)
	}

	for _, bad := range []Filters{
		{AssemblySource: "ensembl"},
		{MAG: "maybe"},
		{AssemblyLevels: []AssemblyLevel{"huge"}},
	} {
		if _, err := newFilters("current", bad); err == nil {
			t.Errorf("filters %+v were accepted", bad)
		}
	}
}

func TestParseFlagValues(t *testing.T) {
	if v, err := ParseAssemblyVersion("Latest"); err != nil || v != VersionCurrent {
		t.Errorf("latest: %v %v", v, err)
	}
	if v, _ := ParseAssemblyVersion(""); v != VersionDefault {
		t.Errorf("empty version: %v", v)
	}
	if v, _ := ParseAssemblyVersion("all"); v != VersionAll {
		t.Errorf("all version: %v", v)
	}
	if _, err := ParseAssemblyVersion("x"); err == nil {
		t.Error("bad version accepted")
	}
	if s, err := ParseAssemblySource("RefSeq"); err != nil || s != SourceRefSeq {
		t.Errorf("refseq: %v %v", s, err)
	}
	if s, _ := ParseAssemblySource(""); s != SourceAll {
		t.Errorf("empty source: %v", s)
	}
	if _, err := ParseAssemblySource("ensembl"); err == nil {
		t.Error("bad source accepted")
	}
	lv, err := ParseAssemblyLevels("Complete, contig")
	if err != nil || !reflect.DeepEqual(lv, []AssemblyLevel{LevelComplete, LevelContig}) {
		t.Errorf("levels: %v %v", lv, err)
	}
	if _, err := ParseAssemblyLevels("complete,huge"); err == nil {
		t.Error("bad level accepted")
	}
	if m, err := ParseMAGFilter("only"); err != nil || m != MAGOnly {
		t.Errorf("mag: %v %v", m, err)
	}
	if m, _ := ParseMAGFilter(""); m != MAGAll {
		t.Errorf("empty mag: %v", m)
	}
	if _, err := ParseMAGFilter("maybe"); err == nil {
		t.Error("bad mag accepted")
	}
}

func TestFiltersSentWithResolve(t *testing.T) {
	var mu sync.Mutex
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(body, &seen)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"reports":[{"accession":"GCA_9.1"}]}`)
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL})
	got, err := reportAccessions(c, Request{
		Accessions: []string{"PRJNA1"},
		Filters:    Filters{AnnotatedOnly: true, AssemblySource: SourceGenBank},
	})
	if err != nil || !reflect.DeepEqual(got, []string{"GCA_9.1"}) {
		t.Fatalf("got %v (%v)", got, err)
	}
	mu.Lock()
	defer mu.Unlock()
	f, _ := seen["filters"].(map[string]any)
	if f["has_annotation"] != true || f["assembly_source"] != "genbank" {
		t.Errorf("filters not sent as requested: %v", f)
	}
	if !reflect.DeepEqual(seen["bioprojects"], []any{"PRJNA1"}) {
		t.Errorf("bioprojects = %v", seen["bioprojects"])
	}
	if _, present := seen["accessions"]; present {
		t.Error("accessions must be absent for a BioProject-only request")
	}
}

func TestInvalidFilterFailsBeforeNetwork(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL})
	_, err := c.Download(context.Background(), Request{
		Accessions: []string{"GCF_000001735.4"},
		Filters:    Filters{AssemblySource: "ensembl"},
	}, io.Discard)
	if err == nil || atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("want an error and no requests, got %v after %d calls", err, calls)
	}
}

// reportAccessions runs AssemblyReports and reduces the reports to accessions.
func reportAccessions(c *Client, req Request) ([]string, error) {
	reports, err := c.AssemblyReports(context.Background(), req)
	if err != nil {
		return nil, err
	}
	return Accessions(reports), nil
}

func TestAssemblyReportsReturnsFullReports(t *testing.T) {
	fixture, err := os.ReadFile("testdata/assembly_data_report_GCF_000001735.4.json")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(body, &seen)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"reports":[%s],"total_count":1}`, fixture)
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL})
	reports, err := c.AssemblyReports(context.Background(), Request{Accessions: []string{"GCF_000001735.4"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(reports))
	}
	r := reports[0]

	mu.Lock()
	rc := seen["returned_content"]
	mu.Unlock()
	if rc != "COMPLETE" {
		t.Errorf("returned_content = %v, want COMPLETE", rc)
	}

	if r.Accession != "GCF_000001735.4" || r.CurrentAccession != "GCF_000001735.4" || r.PairedAccession != "GCA_000001735.2" {
		t.Errorf("accessions: %q %q %q", r.Accession, r.CurrentAccession, r.PairedAccession)
	}
	if r.SourceDatabase != SourceDatabaseRefseq {
		t.Errorf("source database = %q", r.SourceDatabase)
	}
	if r.Organism == nil || r.Organism.TaxID != 3702 || r.Organism.OrganismName != "Arabidopsis thaliana" {
		t.Fatalf("organism = %+v", r.Organism)
	}
	if r.AssemblyInfo == nil || r.AssemblyInfo.AssemblyName != "TAIR10.1" || r.AssemblyInfo.AssemblyStatus != AssemblyStatusCurrent {
		t.Fatalf("assembly info = %+v", r.AssemblyInfo)
	}
	if pa := r.AssemblyInfo.PairedAssembly; pa == nil || pa.Accession != "GCA_000001735.2" {
		t.Errorf("paired assembly = %+v", pa)
	}
	if bs := r.AssemblyInfo.Biosample; bs == nil || bs.Accession != "SAMN03081427" || bs.Ecotype != "Columbia" {
		t.Errorf("biosample = %+v", bs)
	}
	st := r.AssemblyStats
	if st == nil || st.TotalSequenceLength != 119146348 || st.ATGCCount != 118960141 || st.ContigN50 != 11194537 || st.GCPercent != 36 {
		t.Fatalf("assembly stats = %+v", st)
	}
	if r.AnnotationInfo == nil || r.AnnotationInfo.Stats == nil || r.AnnotationInfo.Stats.GeneCounts == nil ||
		r.AnnotationInfo.Stats.GeneCounts.Total != 38312 || r.AnnotationInfo.Stats.GeneCounts.ProteinCoding != 27562 {
		t.Errorf("annotation info = %+v", r.AnnotationInfo)
	}
	if len(r.OrganelleInfo) != 2 || r.OrganelleInfo[0].Description != "Chloroplast" || r.OrganelleInfo[0].TotalSeqLength != 154478 {
		t.Errorf("organelle info = %+v", r.OrganelleInfo)
	}
	if !reflect.DeepEqual(Accessions(reports), []string{"GCF_000001735.4"}) {
		t.Errorf("Accessions = %v", Accessions(reports))
	}

	// 64-bit numbers go back out as strings, and the whole report round-trips.
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"total_sequence_length":"119146348"`) {
		t.Errorf("uint64 must marshal as a string: %s", out)
	}
	if !strings.HasPrefix(string(out), `{"$schema":"`+AssemblyDataReportSchemaURL+`",`) {
		t.Errorf("a marshalled report must start with its $schema reference: %.120s", out)
	}
	if org, _ := json.Marshal(r.Organism); strings.Contains(string(org), "$schema") {
		t.Errorf("only the report itself carries $schema: %s", org)
	}
	var back AssemblyDataReport
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, r) {
		t.Error("report did not survive a marshal/unmarshal round trip")
	}
}

func TestReportDecodingIsLenient(t *testing.T) {
	in := `{"accession":"GCF_1.1","source_database":"SOURCE_DATABASE_FUTURE","brand_new_field":{"x":1},` +
		`"assembly_stats":{"atgc_count":12,"total_sequence_length":"34"}}`
	var r AssemblyDataReport
	if err := json.Unmarshal([]byte(in), &r); err != nil {
		t.Fatal(err)
	}
	if r.SourceDatabase != "SOURCE_DATABASE_FUTURE" {
		t.Errorf("an enum value added later must be kept, got %q", r.SourceDatabase)
	}
	if r.AssemblyStats == nil || r.AssemblyStats.ATGCCount != 12 || r.AssemblyStats.TotalSequenceLength != 34 {
		t.Errorf("Uint64 must accept a number and a string: %+v", r.AssemblyStats)
	}

	var s struct {
		N Uint64 `json:"n"`
	}
	if err := json.Unmarshal([]byte(`{"n":"x"}`), &s); err == nil {
		t.Error("a non-numeric Uint64 was accepted")
	}
}

func TestMarshalReportSchemaReference(t *testing.T) {
	b, err := json.Marshal(AssemblyDataReport{Accession: "GCF_1.1"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"$schema":"` + AssemblyDataReportSchemaURL + `","accession":"GCF_1.1"}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}

	// Pointer, slice and slice-of-pointer forms all carry the reference too.
	for _, v := range []any{
		&AssemblyDataReport{Accession: "A"},
		[]AssemblyDataReport{{Accession: "A"}},
		[]*AssemblyDataReport{{Accession: "A"}},
	} {
		out, err := json.Marshal(v)
		if err != nil || !strings.Contains(string(out), `"$schema":"`+AssemblyDataReportSchemaURL+`"`) {
			t.Errorf("%T: %s (%v)", v, out, err)
		}
	}

	// Decoding ignores the property.
	var back AssemblyDataReport
	if err := json.Unmarshal([]byte(want), &back); err != nil || back.Accession != "GCF_1.1" {
		t.Errorf("decoding a record with $schema: %+v (%v)", back, err)
	}

	if !strings.HasPrefix(AssemblyDataReportSchemaURL, "https://") || !strings.HasSuffix(AssemblyDataReportSchemaURL, ".schema.json") {
		t.Errorf("unexpected schema URL %q", AssemblyDataReportSchemaURL)
	}
}

// checkManifest verifies a manifest against the contents the test zip was built from.
func checkManifest(t *testing.T, m *Manifest, contents map[string]string) {
	t.Helper()
	if m == nil {
		t.Fatal("manifest is nil")
	}
	if len(m.Files) != len(contents) {
		t.Fatalf("manifest lists %d files, want %d: %+v", len(m.Files), len(contents), m.Files)
	}
	var total uint64
	for _, f := range m.Files {
		want, ok := contents[f.Name]
		if !ok {
			t.Errorf("unexpected file %q in manifest", f.Name)
			continue
		}
		if f.Size != uint64(len(want)) {
			t.Errorf("%s: size %d, want %d", f.Name, f.Size, len(want))
		}
		if f.CRC32 != crc32.ChecksumIEEE([]byte(want)) {
			t.Errorf("%s: crc32 %08x", f.Name, f.CRC32)
		}
		if f.CompressedSize == 0 {
			t.Errorf("%s: compressed size is 0", f.Name)
		}
		total += f.Size
	}
	if m.TotalSize() != total {
		t.Errorf("TotalSize = %d, want %d", m.TotalSize(), total)
	}
}

func TestDownloadWriterManifest(t *testing.T) {
	contents := map[string]string{
		"ncbi_dataset/data/assembly_data_report.jsonl": "{}\n",
		"ncbi_dataset/data/GCF_1.1/genomic.gff":        "##gff-version 3\n",
	}
	zipBytes := makeZip(t, zip.Deflate, contents)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/genome/dataset_report" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"reports":[{"accession":"GCF_000001735.4"}]}`)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Write(zipBytes)
	}))
	defer srv.Close()

	spoolDir := t.TempDir()
	c := New(Options{BaseURL: srv.URL, TempDir: spoolDir})
	req := Request{Accessions: []string{"GCF_000001735.4"}}

	// Without Manifest: the stream is untouched, there is no manifest and no temporary file.
	var plain bytes.Buffer
	res, err := c.Download(context.Background(), req, &plain)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest != nil {
		t.Error("Manifest must stay nil unless requested")
	}
	if !bytes.Equal(plain.Bytes(), zipBytes) {
		t.Error("streamed bytes differ from the package")
	}
	if entries, _ := os.ReadDir(spoolDir); len(entries) != 0 {
		t.Errorf("a temporary file was created without Request.Manifest: %v", entries)
	}

	// With Manifest: same bytes, manifest filled, temporary copy removed.
	req.Manifest = true
	var withManifest bytes.Buffer
	res, err = c.Download(context.Background(), req, &withManifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(withManifest.Bytes(), zipBytes) {
		t.Error("streamed bytes differ from the package when a manifest is requested")
	}
	checkManifest(t, res.Manifest, contents)
	if entries, _ := os.ReadDir(spoolDir); len(entries) != 0 {
		t.Errorf("temporary copy left behind: %v", entries)
	}

	// The manifest serialises with stable key names.
	b, err := json.Marshal(res.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"files"`, `"name"`, `"size"`, `"compressed_size"`, `"crc32"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("manifest JSON lacks %s: %s", key, b)
		}
	}
}

func TestDownloadWriterManifestRejectsNonZip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/genome/dataset_report" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"reports":[{"accession":"GCF_000001735.4"}]}`)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		io.WriteString(w, "definitely not a zip")
	}))
	defer srv.Close()

	spoolDir := t.TempDir()
	c := New(Options{BaseURL: srv.URL, TempDir: spoolDir})
	req := Request{Accessions: []string{"GCF_000001735.4"}, Manifest: true}
	_, err := c.Download(context.Background(), req, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("want an error about the manifest, got %v", err)
	}
	if entries, _ := os.ReadDir(spoolDir); len(entries) != 0 {
		t.Errorf("temporary copy left behind after a failure: %v", entries)
	}

	// Download does not validate the archive, so without a manifest the same bytes are accepted.
	req.Manifest = false
	if _, err := c.Download(context.Background(), req, io.Discard); err != nil {
		t.Errorf("without Manifest the stream should pass through: %v", err)
	}
}

func TestReadManifest(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if _, err := zw.Create("dir/"); err != nil {
		t.Fatal(err)
	}
	w, err := zw.Create("dir/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "m.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 1 || m.Files[0].Name != "dir/a.txt" || m.Files[0].Size != 5 {
		t.Errorf("directory entries must be skipped and the file listed: %+v", m.Files)
	}

	if _, err := ReadManifest(filepath.Join(t.TempDir(), "missing.zip")); err == nil {
		t.Error("a missing file was accepted")
	}

	var none *Manifest
	if none.TotalSize() != 0 || none.TotalCompressedSize() != 0 {
		t.Error("a nil manifest must total 0")
	}
}

// countingServer answers the assembly lookup with the given JSON and counts every
// request, so a test can show that a rejected request never reached the API, or
// never got as far as the download.
func countingServer(t *testing.T, reports string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/genome/dataset_report" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, reports)
			return
		}
		http.Error(w, "the download endpoint must not be reached", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestDownloadRejectsInputThatIsNotOneAssemblyAccession(t *testing.T) {
	cases := map[string][]string{
		"two different assemblies":     {"GCF_000001735.4", "GCA_000001735.2"},
		"two versions of one":          {"GCF_000001735.3", "GCF_000001735.4"},
		"a BioProject":                 {"PRJNA10719"},
		"a BioProject and an assembly": {"PRJNA10719", "GCF_000001735.4"},
	}
	for name, accessions := range cases {
		t.Run(name, func(t *testing.T) {
			srv, calls := countingServer(t, `{"reports":[]}`)
			c := New(Options{BaseURL: srv.URL})
			_, err := c.Download(context.Background(), Request{Accessions: accessions}, io.Discard)
			if !errors.Is(err, ErrNotSingleGenome) {
				t.Fatalf("want ErrNotSingleGenome, got %v", err)
			}
			if n := atomic.LoadInt32(calls); n != 0 {
				t.Errorf("a rejected request must not reach the API, got %d requests", n)
			}
		})
	}

	// DownloadToFile follows the same rule and leaves nothing behind.
	srv, calls := countingServer(t, `{"reports":[]}`)
	c := New(Options{BaseURL: srv.URL})
	dir := t.TempDir()
	_, err := c.DownloadToFile(context.Background(), Request{Accessions: []string{"PRJNA10719"}}, filepath.Join(dir, "x.zip"))
	if !errors.Is(err, ErrNotSingleGenome) {
		t.Errorf("DownloadToFile: want ErrNotSingleGenome, got %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a rejected request left files behind: %v", entries)
	}
	if n := atomic.LoadInt32(calls); n != 0 {
		t.Errorf("DownloadToFile: a rejected request must not reach the API, got %d requests", n)
	}

	// No accession at all is an error too.
	if _, err := c.Download(context.Background(), Request{}, io.Discard); err == nil {
		t.Error("an empty request was accepted")
	}
}

func TestDownloadRejectsAccessionThatMatchesSeveralAssemblies(t *testing.T) {
	srv, calls := countingServer(t,
		`{"reports":[{"accession":"GCF_000001735.1"},{"accession":"GCF_000001735.2"},{"accession":"GCF_000001735.3"}]}`)
	c := New(Options{BaseURL: srv.URL})
	_, err := c.Download(context.Background(),
		Request{Accessions: []string{"GCF_000001735.4"}, AssemblyVersion: VersionAll}, io.Discard)
	if !errors.Is(err, ErrNotSingleGenome) {
		t.Fatalf("want ErrNotSingleGenome, got %v", err)
	}
	for _, want := range []string{"3 assemblies", "GCF_000001735.1", "GCF_000001735.3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Errorf("only the lookup may reach the API, got %d requests", n)
	}
}

func TestNotSingleGenomeMessageListsAtMostFive(t *testing.T) {
	err := notSingleGenome([]string{"A1", "A2", "A3", "A4", "A5", "A6", "A7"})
	if !errors.Is(err, ErrNotSingleGenome) {
		t.Fatalf("want ErrNotSingleGenome, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"7 assemblies", "A1", "A5", "and 2 more"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "A6") {
		t.Errorf("message must list at most five accessions: %q", msg)
	}
}

func TestDownloadTreatsRepeatedAccessionAsOneGenome(t *testing.T) {
	zipBytes := makeZip(t, zip.Deflate, map[string]string{"a.txt": "x"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/genome/dataset_report" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"reports":[{"accession":"GCF_000001735.4"}]}`)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Write(zipBytes)
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL})
	res, err := c.Download(context.Background(),
		Request{Accessions: []string{"GCF_000001735.4", "gcf_000001735.4"}}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if res.Accession != "GCF_000001735.4" {
		t.Errorf("Accession = %q", res.Accession)
	}
}
