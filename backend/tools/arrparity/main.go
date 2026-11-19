// Command arrparity measures how closely mediastorm's release evaluation
// matches a real Sonarr or Radarr instance.
//
//	arrparity capture      -app sonarr -url http://127.0.0.1:8989 -key KEY -titles titles.txt -out fixture.json
//	arrparity push-formats -app sonarr -url ... -key ... -trash /path/to/Guides/docs/json
//	arrparity report       -fixture fixture.json [-trash /path/to/Guides/docs/json] [-show 40] [-field languages]
//
// capture queries /api/v3/parse for every title and records the oracle's
// answers (quality, revision, languages, release group, release type, edition
// and matched custom formats). Run push-formats first so the oracle knows the
// TRaSH custom formats. report re-parses the recorded titles with
// mediastorm's engine and prints match rates and mismatches.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"novastream/internal/arr"
	"novastream/internal/arr/arrclient"
	"novastream/internal/trash"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "capture":
		err = capture(os.Args[2:])
	case "push-formats":
		err = pushFormats(os.Args[2:])
	case "report":
		err = report(os.Args[2:])
	case "parse":
		err = parseCmd(os.Args[2:])
	case "bundle":
		err = bundleCmd(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "arrparity:", err)
		os.Exit(1)
	}
}

func parseCmd(args []string) error {
	fs := flag.NewFlagSet("parse", flag.ExitOnError)
	appName := fs.String("app", "sonarr", "sonarr or radarr")
	_ = fs.Parse(args)
	app, err := arr.ParseApp(*appName)
	if err != nil {
		return err
	}
	for _, title := range fs.Args() {
		rel := arr.Parse(app, title)
		data, _ := json.MarshalIndent(rel, "", "  ")
		fmt.Println(string(data))
	}
	return nil
}

func bundleCmd(args []string) error {
	fs := flag.NewFlagSet("bundle", flag.ExitOnError)
	dir := fs.String("trash", "", "TRaSH Guides docs/json directory")
	commit := fs.String("commit", "", "commit SHA of the snapshot")
	out := fs.String("out", "", "output .tar.gz path")
	_ = fs.Parse(args)
	if *dir == "" || *commit == "" || *out == "" {
		return fmt.Errorf("-trash, -commit and -out are required")
	}
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	if err := trash.BuildBundle(*dir, *commit, f); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	snap, err := trash.LoadArchiveFile(*out)
	if err != nil {
		return fmt.Errorf("verify bundle: %w", err)
	}
	fmt.Printf("bundle %s: commit %s, %d sonarr / %d radarr formats\n", *out, snap.Commit, len(snap.App("sonarr").CustomFormats), len(snap.App("radarr").CustomFormats))
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: arrparity capture|push-formats|report|parse|bundle [flags]")
	os.Exit(2)
}

func readTitles(paths []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			t := strings.TrimSpace(sc.Text())
			if t == "" || strings.HasPrefix(t, "#") || seen[t] {
				continue
			}
			seen[t] = true
			out = append(out, t)
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func capture(args []string) error {
	fs := flag.NewFlagSet("capture", flag.ExitOnError)
	appName := fs.String("app", "sonarr", "sonarr or radarr")
	baseURL := fs.String("url", "", "instance URL")
	key := fs.String("key", "", "API key")
	titlesArg := fs.String("titles", "", "comma-separated title list files")
	out := fs.String("out", "", "fixture output path")
	trashCommit := fs.String("trash-commit", "", "TRaSH commit the oracle's formats came from")
	workers := fs.Int("workers", 8, "parallel requests")
	_ = fs.Parse(args)
	app, err := arr.ParseApp(*appName)
	if err != nil {
		return err
	}
	client, err := arrclient.New(*baseURL, *key, nil)
	if err != nil {
		return err
	}
	ctx := context.Background()
	status, err := client.SystemStatus(ctx)
	if err != nil {
		return fmt.Errorf("oracle status: %w", err)
	}
	if got, _ := status.App(); got != app {
		return fmt.Errorf("oracle is %s, not %s", status.AppName, app)
	}
	titles, err := readTitles(strings.Split(*titlesArg, ","))
	if err != nil {
		return err
	}
	obs := make([]arr.ParityObservation, len(titles))
	var wg sync.WaitGroup
	jobs := make(chan int)
	var mu sync.Mutex
	var failures []string
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				var o arr.ParityObservation
				var err error
				for attempt := 0; attempt < 3; attempt++ {
					o, err = client.Parse(ctx, titles[i])
					if err == nil {
						break
					}
					time.Sleep(200 * time.Millisecond)
				}
				if err != nil {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("%s: %v", titles[i], err))
					mu.Unlock()
				}
				obs[i] = o
			}
		}()
	}
	for i := range titles {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	fx := &arr.ParityFixture{App: app, OracleVersion: status.AppName + " " + status.Version, TrashCommit: *trashCommit}
	for _, o := range obs {
		if o.Title != "" {
			fx.Observations = append(fx.Observations, o)
		}
	}
	if err := fx.Save(*out); err != nil {
		return err
	}
	parsed := 0
	for _, o := range fx.Observations {
		if o.Parsed {
			parsed++
		}
	}
	fmt.Printf("captured %d titles (%d parsed, %d failed) from %s\n", len(fx.Observations), parsed, len(failures), fx.OracleVersion)
	for i, f := range failures {
		if i >= 10 {
			break
		}
		fmt.Println("  failed:", f)
	}
	return nil
}

func loadTrashFormats(dir string, app arr.App) ([]arr.CustomFormat, string, error) {
	snap, err := trash.LoadDir(dir)
	if err != nil {
		return nil, "", err
	}
	return snap.CustomFormats(app), snap.Commit, nil
}

func pushFormats(args []string) error {
	fs := flag.NewFlagSet("push-formats", flag.ExitOnError)
	appName := fs.String("app", "sonarr", "sonarr or radarr")
	baseURL := fs.String("url", "", "instance URL")
	key := fs.String("key", "", "API key")
	dir := fs.String("trash", "", "TRaSH Guides docs/json directory")
	_ = fs.Parse(args)
	app, err := arr.ParseApp(*appName)
	if err != nil {
		return err
	}
	client, err := arrclient.New(*baseURL, *key, nil)
	if err != nil {
		return err
	}
	formats, _, err := loadTrashFormats(*dir, app)
	if err != nil {
		return err
	}
	ctx := context.Background()
	existing, err := client.ListCustomFormats(ctx)
	if err != nil {
		return err
	}
	byName := map[string]int{}
	for _, e := range existing {
		byName[strings.ToLower(e.Format.Name)] = e.ID
	}
	created, updated := 0, 0
	var problems []string
	for _, cf := range formats {
		if remoteID, ok := byName[strings.ToLower(cf.Name)]; ok {
			if err := client.UpdateCustomFormat(ctx, remoteID, cf); err != nil {
				problems = append(problems, fmt.Sprintf("update %s: %v", cf.Name, err))
				continue
			}
			updated++
			continue
		}
		if _, err := client.CreateCustomFormat(ctx, cf); err != nil {
			problems = append(problems, fmt.Sprintf("create %s: %v", cf.Name, err))
			continue
		}
		created++
	}
	fmt.Printf("pushed %d formats (%d created, %d updated, %d failed)\n", len(formats), created, updated, len(problems))
	for _, p := range problems {
		fmt.Println("  ", p)
	}
	return nil
}

func report(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	path := fs.String("fixture", "", "fixture path")
	dir := fs.String("trash", "", "TRaSH Guides docs/json directory (enables custom format comparison)")
	show := fs.Int("show", 30, "mismatches to print per field")
	only := fs.String("field", "", "only print mismatches of this field")
	tsv := fs.Bool("tsv", false, "print mismatches as title<TAB>field<TAB>want<TAB>got")
	_ = fs.Parse(args)
	fx, err := arr.LoadParityFixture(*path)
	if err != nil {
		return err
	}
	var set *arr.FormatSet
	if *dir != "" {
		formats, _, err := loadTrashFormats(*dir, fx.App)
		if err != nil {
			return err
		}
		var errs []error
		set, errs = arr.NewFormatSet(fx.App, formats)
		for _, e := range errs {
			fmt.Println("compile:", e)
		}
	}
	rep := arr.CompareParity(fx, set)
	if *tsv {
		for _, m := range rep.Mismatches {
			if *only == "" || *only == string(m.Field) {
				fmt.Printf("%s\t%s\t%s\t%s\n", m.Title, m.Field, m.Want, m.Got)
			}
		}
		return nil
	}
	fmt.Print(rep.Summary())
	byField := map[arr.ParityField][]arr.ParityMismatch{}
	for _, m := range rep.Mismatches {
		byField[m.Field] = append(byField[m.Field], m)
	}
	fields := make([]string, 0, len(byField))
	for f := range byField {
		fields = append(fields, string(f))
	}
	sort.Strings(fields)
	for _, f := range fields {
		if *only != "" && *only != f {
			continue
		}
		list := byField[arr.ParityField(f)]
		fmt.Printf("\n== %s mismatches (%d)\n", f, len(list))
		for i, m := range list {
			if i >= *show {
				break
			}
			fmt.Printf("  %s\n      want: %s\n      got:  %s\n", m.Title, m.Want, m.Got)
		}
	}
	return nil
}
