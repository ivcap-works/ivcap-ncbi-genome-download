# Assembly data report JSON Schemas

Two JSON Schemas (draft 2020-12) for NCBI Datasets' genome assembly data report,
generated from NCBI's OpenAPI definition `v2reportsAssemblyDataReport` and the 34
definitions it references, in the `datasets.openapi.yaml` of
[github.com/ncbi/datasets](https://github.com/ncbi/datasets).

| File | Property names | Matches |
|---|---|---|
| `assembly-data-report.schema.json` | snake_case (`assembly_info`) | REST API responses and `datasets summary` output |
| `assembly-data-report.package.schema.json` | camelCase (`assemblyInfo`) | `ncbi_dataset/data/assembly_data_report.jsonl` in a data package (one record per line) |

Their `$id`s are:

- `https://raw.githubusercontent.com/ivcap-works/ivcap-ncbi-genome-download/main/schema/assembly-data-report.schema.json`
- `https://raw.githubusercontent.com/ivcap-works/ivcap-ncbi-genome-download/main/schema/assembly-data-report.package.schema.json`

An `$id` is an identifier; these URLs only resolve once the files are pushed to
the `main` branch of that repository. To publish them elsewhere, regenerate with
`--schema-base-url` (see below), which also updates the Go constant.

A record can say which schema it follows through a top-level `"$schema"`
property. The Go type `AssemblyDataReport` writes one automatically (the
snake_case `$id`), and both schemas declare it as an optional string property.

The structure is identical; only the property names differ (the camelCase form is
protobuf's JSON naming of the same fields). The Go types in `../report_types.go`
match the snake_case form.

NCBI's own documentation of the report is at
https://www.ncbi.nlm.nih.gov/datasets/docs/v2/reference-docs/data-reports/genome-assembly/

## What the schemas do and do not check

- They are derived from NCBI's OpenAPI definition. They are **not** official NCBI schemas.
- Nothing is required and unknown properties are allowed, as in the source definition. They check the types, nesting and enum values of the properties that are present, not completeness.
- Enums are closed, so a value NCBI adds later fails validation. (The Go enum types stay open.)
- NCBI sends 64-bit integers as strings; the schemas require `type: string` and, as an addition to the source definition, the pattern `^[0-9]+$` for them.
- `format` values (`float`, `uint64`) are annotations only. Integers carry no range limits.
- The root declares an optional `"$schema"` property (the URL of the schema a record follows). That is an addition to NCBI's definition: NCBI's own records do not contain it.

## Provenance and validation

Generated from ncbi/datasets commit `488962028aab2a1bab244dd35def300783cebf69`
(`datasets.openapi.yaml` sha256 `353e226daee7425064f7ee2267079d591ad36e8c0cef1b86b433ece08ed6f840`).
The same values are in each schema's `$comment`.

Checked with `jsonschema` 3.2.0 using Draft 7 rules, which agree with 2020-12 for
the keywords used here (`type`, `properties`, `items`, `enum`, `pattern`, `$ref`);
a 2020-12 validator has not been run yet:

- both schemas are well-formed;
- the record in `../testdata/` validates against the snake_case schema, and the
  `assembly_data_report.jsonl` record of the same assembly from a real package
  validates against the camelCase schema, with every one of the 89 keys recognised;
- nine deliberately broken records (wrong types, a bad enum value, a 64-bit
  number sent as a JSON number, a non-numeric 64-bit string, a wrong nested type)
  are all rejected.

## Regenerating

```
python3 tools/genreport/generate.py \
    --spec path/to/ncbi/datasets/datasets.openapi.yaml \
    --commit <git commit of that checkout> --out . \\
    [--schema-base-url https://example.org/schemas/]
```

Requires PyYAML. The script stops on any spec construct it does not know, so a
changed spec cannot be converted silently and wrongly. It also rewrites
`report_types.go`, including the `AssemblyDataReportSchemaURL` constant.
