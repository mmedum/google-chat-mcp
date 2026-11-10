package main

import (
	"fmt"
	"maps"
	"os"
	"path"
	"slices"
	"strings"
	"testing"
)

// The release attests its archives, the bundle and the checksum file
// goreleaser writes, in the job that publishes them. actions/attest v4.2.1,
// which attest-build-provenance v4.2.2 runs, splits subject-path at commas
// and newlines and trims each glob
// (https://github.com/actions/attest/blob/508db95dd578ae2727ebd6217d5ba78e4fbda05d/src/subject.ts#L303),
// so a space-separated list is one glob that matches nothing.
func TestTheReleaseAttestsItsArchivesBundleAndChecksumFile(t *testing.T) {
	t.Chdir("../..")
	flows, err := readWorkflows()
	if err != nil {
		t.Fatal(err)
	}
	release, ok := workflowNamed(flows, releaseWorkflow)
	if !ok {
		t.Fatal("no " + releaseWorkflow)
	}
	cfg, err := os.ReadFile(parityGoreleaser)
	if err != nil {
		t.Fatal(err)
	}
	if p := releaseAttestProblems(release, string(cfg)); p != nil {
		t.Errorf("the release: %q", p)
	}
}

// releaseAttestProblems holds each job of the release workflow that runs
// goreleaser release to a build-provenance step of its own, whose token can
// write the attestation, naming releaseFiles' entries. A step in another job
// or workflow can be skipped while the release ships, and actions/attest
// needs id-token: write and attestations: write to sign and store the
// provenance
// (https://github.com/actions/attest/blob/508db95dd578ae2727ebd6217d5ba78e4fbda05d/README.md#L64-L76).
func releaseAttestProblems(release workflowFile, cfg string) []string {
	files, problems := releaseFiles(cfg)
	jobs := releaseJobs(release)
	if len(jobs) == 0 {
		return append(problems, release.path+" has no job that runs goreleaser-action with args starting `release`, "+
			"so no attestation is held to what the release publishes")
	}
	for _, job := range jobs {
		attested := false
		for _, s := range release.steps {
			if s.job != job || s.action() != "actions/attest-build-provenance" {
				continue
			}
			attested = true
			subjects, ok := s.with["subject-path"]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s:%d: the attestation names no subject-path", release.path, s.line))
				continue
			}
			problems = append(problems, releaseSubjectProblems(subjects.value, files)...)
		}
		if !attested {
			problems = append(problems, fmt.Sprintf("%s: job %s runs goreleaser release and no build-provenance attestation",
				release.path, job))
			continue
		}
		for _, scope := range []string{"id-token", "attestations"} {
			if !release.grantsWrite(job, scope) {
				problems = append(problems, fmt.Sprintf("%s: job %s attests without %s: write; grant it in the job's "+
					"permissions, or the workflow's when the job sets none", release.path, job, scope))
			}
		}
	}
	return problems
}

// releaseJobs are the jobs of w that publish the release, in file order:
// each runs goreleaser-action with args starting `release`.
func releaseJobs(w workflowFile) []string {
	var jobs []string
	for _, s := range w.steps {
		args, _ := s.input("args")
		words := strings.Fields(args.value)
		if s.action() == "goreleaser/goreleaser-action" && len(words) > 0 && words[0] == "release" && !slices.Contains(jobs, s.job) {
			jobs = append(jobs, s.job)
		}
	}
	return jobs
}

// releaseFiles is the subject-path entry for each file the attestation
// has to name: in goreleaser's dist directory, a glob for an archive in
// each format an archive is built in and checksum.name_template's file,
// and each .mcpb glob release.extra_files uploads, which is where the
// bundle's hook writes it. The release publishes SBOMs and the checksum
// file's signature too, which are not held.
func releaseFiles(cfg string) (files, problems []string) {
	g := parseGoreleaser(cfg)
	problems = append(problems, g.refused...)
	lines := strings.Split(strings.ReplaceAll(cfg, "\r\n", "\n"), "\n")
	dist, checksum := "dist", ""
	// archives are by the index of the archives: item each value is under.
	archives := map[int]*releaseArchive{}
	var bundles []string
	for _, v := range g.values {
		switch v.keys {
		case "dist", "checksum.name_template", "release.extra_files.glob":
			// A block scalar keeps the line break after its text, which no
			// file the release writes is named with.
			if key := v.keys[strings.LastIndexByte(v.keys, '.')+1:]; !strings.Contains(lines[v.line-1], key+":") {
				problems = append(problems, fmt.Sprintf("%s:%d: %s: a block scalar; write the value on the key's line",
					parityGoreleaser, v.line-1, key))
				continue
			}
		}
		var a *releaseArchive
		if strings.HasPrefix(v.keys, "archives.") && len(v.items) > 0 {
			if a = archives[v.items[0]]; a == nil {
				a = &releaseArchive{}
				archives[v.items[0]] = a
			}
		}
		switch v.keys {
		case "dist":
			dist = v.value
		case "checksum.name_template":
			checksum = v.value
		case "release.extra_files.glob":
			if strings.HasSuffix(v.value, ".mcpb") {
				bundles = append(bundles, path.Clean(v.value))
			}
		case "archives.formats", "archives.format", "archives.format_overrides.formats", "archives.format_overrides.format":
			switch {
			case a == nil:
				problems = append(problems, fmt.Sprintf("%s:%d: %s is not in an item of archives, which goreleaser reads as a list",
					parityGoreleaser, v.line, v.keys))
			case strings.HasPrefix(v.keys, "archives.format_overrides."):
				a.overrides = append(a.overrides, v)
			default:
				a.formats = append(a.formats, v)
			}
		}
	}
	// goreleaser builds one archive for a config that names none, and a
	// tar.gz for an archive with no format of its own
	// (https://github.com/goreleaser/goreleaser/blob/v2.18.1/internal/pipe/archive/archive.go#L57-L68).
	// none builds no archive, and binary uploads the bare binary
	// (https://github.com/goreleaser/goreleaser/blob/v2.18.1/internal/pipe/archive/archive.go#L138-L150).
	if len(archives) == 0 {
		archives[0] = &releaseArchive{}
	}
	var formats []string
	for _, i := range slices.Sorted(maps.Keys(archives)) {
		a := archives[i]
		if len(a.formats) == 0 {
			a.formats = []goreleaserValue{{value: "tar.gz"}}
		}
		for _, f := range append(a.formats, a.overrides...) {
			switch {
			case f.value == "binary":
				problems = append(problems, fmt.Sprintf("%s:%d: binary: an archive format that uploads the bare binary, "+
					"which no fixed subject-path names; archive it as tar.gz or zip", parityGoreleaser, f.line))
			case f.value != "" && f.value != "none" && !slices.Contains(formats, f.value):
				formats = append(formats, f.value)
				files = append(files, path.Join(dist, "*."+f.value))
			}
		}
	}
	files = append(files, bundles...)
	if len(bundles) == 0 {
		problems = append(problems, "release.extra_files uploads no .mcpb file, so nothing holds the subject-path to the bundle")
	}
	// Without a name_template goreleaser names the checksum file for the
	// project and version, which a fixed subject-path does not match.
	if checksum != "" {
		files = append(files, path.Join(dist, checksum))
	} else {
		problems = append(problems, "checksum.name_template is empty, so nothing holds the subject-path to the "+
			"file every other artifact is verified through")
	}
	return files, problems
}

// releaseArchive is one archive's own formats, and its format overrides'.
type releaseArchive struct{ formats, overrides []goreleaserValue }

// releaseSubjectProblems holds the attestation's subject-path, a comma-
// or newline-separated list, to name each of files as an entry of its own.
// A space-separated value is one literal glob.
func releaseSubjectProblems(subjects string, files []string) []string {
	var problems []string
	parts := strings.FieldsFunc(subjects, func(r rune) bool { return r == ',' || r == '\n' })
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	if slices.ContainsFunc(parts, func(p string) bool { return strings.ContainsAny(p, " \t") }) {
		problems = append(problems, fmt.Sprintf("subject-path %q separates with a space, which is read as one "+
			"glob; separate with commas or newlines", subjects))
	}
	// actions/attest skips a glob that matches nothing while another
	// matches, so dist/unused/*.mcpb attests no bundle and
	// dist/*.mcpb.sigstore.json only a signature
	// (https://github.com/actions/attest/blob/508db95dd578ae2727ebd6217d5ba78e4fbda05d/src/subject.ts#L150).
	for _, want := range files {
		if !slices.Contains(parts, want) {
			problems = append(problems, fmt.Sprintf("subject-path %q does not name %s, so it carries no attestation",
				subjects, want))
		}
	}
	return problems
}

func TestReleaseSubjectProblems(t *testing.T) {
	files := []string{"dist/*.tar.gz", "dist/*.zip", "dist/*.mcpb", "dist/checksums.txt"}
	for name, subjects := range map[string]string{
		"commas":                        "dist/*.tar.gz,dist/*.zip,dist/*.mcpb,dist/checksums.txt",
		"newlines":                      "dist/*.tar.gz\ndist/*.zip\ndist/checksums.txt\ndist/*.mcpb",
		"commas and a space after each": "dist/*.tar.gz, dist/*.zip, dist/checksums.txt, dist/*.mcpb",
	} {
		if p := releaseSubjectProblems(subjects, files); len(p) != 0 {
			t.Errorf("%s: subjects refused: %v", name, p)
		}
	}
	// Each entry ends with a file's name and none is that file's: a
	// signature, a folder no file is in, and a checksum file of another name.
	const subjects = "dist/*.tar.gz,dist/*.zip.sigstore.json,dist/unused/*.mcpb,dist/old-checksums.txt"
	want := []string{
		`subject-path "` + subjects + `" does not name dist/*.zip, so it carries no attestation`,
		`subject-path "` + subjects + `" does not name dist/*.mcpb, so it carries no attestation`,
		`subject-path "` + subjects + `" does not name dist/checksums.txt, so it carries no attestation`,
	}
	if p := releaseSubjectProblems(subjects, files); !slices.Equal(p, want) {
		t.Errorf("problems\n got %q\nwant %q", p, want)
	}
}

// The step is read from the workflow as written: a literal block is a list
// and a folded one is one line of space-separated globs. Each file is named
// by an entry of its own: an archive and the checksum file in goreleaser's
// dist directory, and the bundle where release.extra_files uploads it from.
func TestReleaseAttestProblems(t *testing.T) {
	const (
		archives = "archives:\n  - ids: [server]\n    formats: [tar.gz]\n    format_overrides:\n      - goos: windows\n        formats: [zip] # a zip opens on Windows\n"
		release  = "release:\n  name_template: x\n  extra_files:\n    - glob: ./dist/*.mcpb # the bundle\n"
		checksum = "checksum:\n  # the file the signature covers\n  name_template: 'checksums.txt' # fixed\n"
		cfg      = archives + release + checksum
		all      = "dist/*.tar.gz,dist/*.zip,dist/*.mcpb,dist/checksums.txt"
	)
	const job = "permissions:\n  contents: read\njobs:\n  r:\n    permissions:\n      id-token: write\n      attestations: write\n" +
		"    steps:\n      - uses: goreleaser/goreleaser-action@x\n        with:\n          args: release --clean\n"
	step := func(with string) workflowFile {
		return parseWorkflow("release.yml", job+"      - uses: actions/attest-build-provenance@x\n"+with)
	}
	subjects := func(s string) workflowFile { return step("        with:\n          subject-path: " + s + "\n") }
	missing := func(subjects, what string) string {
		return fmt.Sprintf("subject-path %q does not name %s, so it carries no attestation", subjects, what)
	}
	const folded = "dist/*.tar.gz dist/*.zip dist/*.mcpb dist/checksums.txt"
	for _, tc := range []struct {
		name string
		w    workflowFile
		cfg  string
		want []string
	}{
		{"a literal block", step("        with:\n          subject-path: |\n            dist/*.tar.gz\n            dist/*.zip\n            dist/*.mcpb\n            dist/checksums.txt\n"), cfg, nil},
		{"a folded block", step("        with:\n          subject-path: >\n            dist/*.tar.gz\n            dist/*.zip\n            dist/*.mcpb\n            dist/checksums.txt\n"), cfg, []string{
			`subject-path "` + folded + `" separates with a space, which is read as one glob; separate with commas or newlines`,
			missing(folded, "dist/*.tar.gz"), missing(folded, "dist/*.zip"), missing(folded, "dist/*.mcpb"), missing(folded, "dist/checksums.txt"),
		}},
		{"another checksum file", subjects(all), archives + release + "checksum:\n  name_template: '{{ .ProjectName }}_checksums.txt'\n",
			[]string{missing(all, "dist/{{ .ProjectName }}_checksums.txt")}},
		{"no checksum name, only another block's", subjects("dist/*.tar.gz,dist/*.zip,dist/*.mcpb"),
			archives + release + "checksum:\n  algorithm: sha256\n", []string{
				"checksum.name_template is empty, so nothing holds the subject-path to the file every other artifact is verified through",
			}},
		{"a checksum name only under another key of checksum", subjects("dist/*.tar.gz,dist/*.zip,dist/*.mcpb"),
			archives + release + "checksum:\n  extra_files:\n    - glob: x\n      name_template: other.txt\n", []string{
				"checksum.name_template is empty, so nothing holds the subject-path to the file every other artifact is verified through",
			}},
		{"a tab before each comment", subjects(all), archives + release + "checksum:\t# signed\n  name_template: checksums.txt\t# fixed\n", nil},
		{"no subject-path", step(""), cfg, []string{"release.yml:12: the attestation names no subject-path"}},
		{"no attestation", parseWorkflow("release.yml", job+"      - run: true\n"), cfg,
			[]string{"release.yml: job r runs goreleaser release and no build-provenance attestation"}},
		// The attestation is held in the job that publishes what it attests:
		// one in another job, or another workflow, can be skipped while the
		// release ships.
		{"no job runs goreleaser release", parseWorkflow("release.yml", strings.Replace(job, "args: release --clean", "args: check", 1)+
			"      - uses: actions/attest-build-provenance@x\n        with:\n          subject-path: "+all+"\n"), cfg,
			[]string{"release.yml has no job that runs goreleaser-action with args starting `release`, " +
				"so no attestation is held to what the release publishes"}},
		{"the attestation in another job", parseWorkflow("release.yml", job+"  attest:\n    permissions:\n      id-token: write\n"+
			"      attestations: write\n    steps:\n      - uses: actions/attest-build-provenance@x\n        with:\n          subject-path: "+all+"\n"), cfg,
			[]string{"release.yml: job r runs goreleaser release and no build-provenance attestation"}},
		// actions/attest signs with the job's OIDC token and stores the
		// provenance with its attestations scope.
		{"a job that cannot write the attestation", parseWorkflow("release.yml", strings.Replace(job,
			"      id-token: write\n      attestations: write\n", "      contents: write\n", 1)+
			"      - uses: actions/attest-build-provenance@x\n        with:\n          subject-path: "+all+"\n"), cfg, []string{
			"release.yml: job r attests without id-token: write; grant it in the job's permissions, or the workflow's when the job sets none",
			"release.yml: job r attests without attestations: write; grant it in the job's permissions, or the workflow's when the job sets none",
		}},
		{"the job's permissions replace the workflow's", parseWorkflow("release.yml", strings.Replace(
			strings.Replace(job, "  contents: read\n", "  id-token: write\n  attestations: write\n", 1),
			"      id-token: write\n      attestations: write\n", "      contents: write\n", 1)+
			"      - uses: actions/attest-build-provenance@x\n        with:\n          subject-path: "+all+"\n"), cfg, []string{
			"release.yml: job r attests without id-token: write; grant it in the job's permissions, or the workflow's when the job sets none",
			"release.yml: job r attests without attestations: write; grant it in the job's permissions, or the workflow's when the job sets none",
		}},
		{"the workflow's permissions, the job setting none", parseWorkflow("release.yml", strings.Replace(
			strings.Replace(job, "  contents: read\n", "  id-token: write\n  attestations: write\n", 1),
			"    permissions:\n      id-token: write\n      attestations: write\n", "", 1)+
			"      - uses: actions/attest-build-provenance@x\n        with:\n          subject-path: "+all+"\n"), cfg, nil},
		{"write-all for the job", parseWorkflow("release.yml", strings.Replace(job,
			"    permissions:\n      id-token: write\n      attestations: write\n", "    permissions: write-all\n", 1)+
			"      - uses: actions/attest-build-provenance@x\n        with:\n          subject-path: "+all+"\n"), cfg, nil},
		// actions/attest skips a glob that matches nothing while another
		// matches, so an entry in another folder attests nothing and a
		// signature's entry attests the signature.
		{"the bundle in another folder", subjects("dist/*.tar.gz,dist/*.zip,dist/unused/*.mcpb,dist/checksums.txt"), cfg,
			[]string{missing("dist/*.tar.gz,dist/*.zip,dist/unused/*.mcpb,dist/checksums.txt", "dist/*.mcpb")}},
		{"a signature where the bundle goes", subjects("dist/*.tar.gz,dist/*.zip,dist/*.mcpb.sigstore.json,dist/checksums.txt"), cfg,
			[]string{missing("dist/*.tar.gz,dist/*.zip,dist/*.mcpb.sigstore.json,dist/checksums.txt", "dist/*.mcpb")}},
		{"an archive and the checksum file in other folders", subjects("out/*.tar.gz,dist/*.zip,dist/*.mcpb,dist/sub/checksums.txt"), cfg,
			[]string{
				missing("out/*.tar.gz,dist/*.zip,dist/*.mcpb,dist/sub/checksums.txt", "dist/*.tar.gz"),
				missing("out/*.tar.gz,dist/*.zip,dist/*.mcpb,dist/sub/checksums.txt", "dist/checksums.txt"),
			}},
		// goreleaser writes its archives and the checksum file into its dist
		// directory, and the bundle's hook writes the bundle where
		// release.extra_files uploads it from, so a config that moves the
		// dist directory moves every entry but the bundle's.
		{"another dist directory", subjects(all), "dist: ./out # moved\n" + cfg, []string{
			missing(all, "out/*.tar.gz"), missing(all, "out/*.zip"), missing(all, "out/checksums.txt"),
		}},
		{"the entries in another dist directory", subjects("out/*.tar.gz,out/*.zip,dist/*.mcpb,out/checksums.txt"), "dist: out\n" + cfg, nil},
		{"the bundle uploaded from another folder", subjects(all), strings.Replace(cfg, "./dist/*.mcpb", "./out/*.mcpb", 1),
			[]string{missing(all, "out/*.mcpb")}},
		{"no bundle uploaded", subjects(all), archives + "release:\n  name_template: x\n" + checksum,
			[]string{"release.extra_files uploads no .mcpb file, so nothing holds the subject-path to the bundle"}},
		{"another file uploaded beside the bundle", subjects(all), strings.Replace(cfg, "    - glob: ./dist/*.mcpb", "    - glob: ./NOTICE\n    - glob: ./dist/*.mcpb", 1), nil},
		{"an empty dist", subjects(all), "dist: ''\n" + cfg, nil},
		{"a checksum name as a block scalar", subjects(all), archives + release + "checksum:\n  name_template: |\n    checksums.txt\n", []string{
			".goreleaser.yaml:12: name_template: a block scalar; write the value on the key's line",
			"checksum.name_template is empty, so nothing holds the subject-path to the file every other artifact is verified through",
		}},
		{"a checksum name with an escape", subjects(all), archives + release + "checksum:\n  name_template: \"checksums\\x2etxt\"\n", []string{
			".goreleaser.yaml:12: name_template: a quoted value with an escape in it; write it unquoted or as a block",
			"checksum.name_template is empty, so nothing holds the subject-path to the file every other artifact is verified through",
		}},
		{"an archive format the attestation does not name", subjects(all),
			strings.Replace(cfg, "formats: [tar.gz]", "formats: [tar.gz, tar.zst]", 1),
			[]string{missing(all, "dist/*.tar.zst")}},
		// A format two archives share is one entry to name.
		{"a format two archives share, left out", subjects("dist/*.mcpb,dist/checksums.txt"),
			strings.Replace(cfg, "formats: [zip]", "formats: [tar.gz]", 1),
			[]string{missing("dist/*.mcpb,dist/checksums.txt", "dist/*.tar.gz")}},
		// A one-line flow sequence is read only when it holds plain words.
		{"a quoted format in a flow sequence", subjects(all), strings.Replace(cfg, "formats: [zip]", `formats: ["zip"]`, 1),
			[]string{".goreleaser.yaml:6: formats: a flow collection; write it as a block, one key or item a line"}},
		{"a quoted format one item a line", subjects(all), strings.Replace(cfg, "formats: [zip]", "formats:\n          - \"zip\"", 1), nil},
		{"a format as a scalar", subjects(all), strings.Replace(cfg, "formats: [zip]", "formats: zip", 1), nil},
		{"a formats list one item a line", subjects(all), strings.Replace(cfg, "formats: [zip]", "formats:\n          - zip", 1), nil},
		// nfpms: names package formats, not archive formats.
		{"a formats list outside archives", subjects(all), cfg + "nfpms:\n  - formats: [deb]\n", nil},
		{"archives at their key's column", subjects(all),
			"archives:\n- formats: [tar.gz]\n  format_overrides:\n  - goos: windows\n    formats: [zip]\n" + release + "checksum:\n  name_template: checksums.txt\n", nil},
		{"archives as a mapping", subjects(all), "archives:\n  formats: zip\n" + release + checksum,
			[]string{".goreleaser.yaml:2: archives.formats is not in an item of archives, which goreleaser reads as a list"}},
		// goreleaser builds a tar.gz for a config with no archive, and for an
		// archive with no format.
		{"no archives", subjects("dist/*.mcpb,dist/checksums.txt"), release + checksum,
			[]string{missing("dist/*.mcpb,dist/checksums.txt", "dist/*.tar.gz")}},
		{"an empty formats list", subjects("dist/*.zip,dist/*.mcpb,dist/checksums.txt"), strings.Replace(cfg, "formats: [tar.gz]", "formats: []", 1),
			[]string{missing("dist/*.zip,dist/*.mcpb,dist/checksums.txt", "dist/*.tar.gz")}},
		{"an archive with no format beside one with", subjects("dist/*.tar.zst,dist/*.mcpb,dist/checksums.txt"),
			"archives:\n  - ids: [server]\n    formats: [tar.zst]\n  - ids: [other]\n" + release + checksum,
			[]string{missing("dist/*.tar.zst,dist/*.mcpb,dist/checksums.txt", "dist/*.tar.gz")}},
		// none builds no archive, and binary uploads the bare binary, which
		// no entry in the dist directory names.
		{"an override to none", subjects("dist/*.tar.gz,dist/*.mcpb,dist/checksums.txt"), strings.Replace(cfg, "formats: [zip]", "formats: [none]", 1), nil},
		{"an override to binary", subjects(all), strings.Replace(cfg, "formats: [zip]", "formats: [binary]", 1),
			[]string{".goreleaser.yaml:6: binary: an archive format that uploads the bare binary, which no fixed subject-path names; archive it as tar.gz or zip"}},
		{"a formats list over two lines", subjects(all), strings.Replace(cfg, "formats: [tar.gz]", "formats: [tar.gz,\n      tar.zst]", 1),
			[]string{
				".goreleaser.yaml:3: formats: a flow collection; write it as a block, one key or item a line",
				".goreleaser.yaml:4: archives: a value continued on the next line; write it on one line or as a block scalar",
			}},
		{"an archive from an alias", subjects(all), strings.Replace(cfg, "  - ids: [server]\n", "  - *server\n  - ids: [server]\n", 1),
			[]string{".goreleaser.yaml:2: archives: an anchor, an alias or a tag; write the value out"}},
		{"a quoted archives key", subjects(all), strings.Replace(cfg, "archives:", `"archives":`, 1),
			[]string{`.goreleaser.yaml:1: "archives": a quoted key; write the key plain`}},
		// goreleaser reads the singular format beside formats, deprecated.
		{"an archive's and an override's singular format", subjects("dist/*.tar.gz,dist/*.mcpb,dist/checksums.txt"),
			strings.Replace(strings.Replace(cfg, "formats: [tar.gz]", "format: tar.zst", 1), "formats: [zip]", "format: zip", 1),
			[]string{missing("dist/*.tar.gz,dist/*.mcpb,dist/checksums.txt", "dist/*.tar.zst"), missing("dist/*.tar.gz,dist/*.mcpb,dist/checksums.txt", "dist/*.zip")}},
		{"a quoted singular format, a comma in its comment", subjects(all),
			strings.Replace(cfg, "formats: [zip] # a zip opens on Windows", `format: "zip" # zip, for Windows`, 1), nil},
		{"a singular format on the line after its key", subjects(all), strings.Replace(cfg, "formats: [zip]", "format:\n          zip", 1),
			[]string{".goreleaser.yaml:7: format: a value continued on the next line; write it on one line or as a block scalar"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := releaseAttestProblems(tc.w, tc.cfg); !slices.Equal(got, tc.want) {
				t.Errorf("problems:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(tc.want, "\n  "))
			}
		})
	}
}
