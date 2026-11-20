package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/easydev-org/easyvm/internal/walk"
	"gopkg.in/yaml.v3"
)

// cmdWalk converts walkthroughs between the two formats.
//
//	easyvm walk FILE.yaml         write FILE.walk beside it
//	easyvm walk --check FILE...   convert and convert back, and refuse a difference
//
// THE `--check` FORM IS THE ONE THAT MATTERS FOR A MIGRATION: it proves the `.walk` a person is about
// to commit holds the same document as the YAML it replaces, which is the only way to convert sixteen
// files and 6675 lines without reading all of it.
func cmdWalk(args []string) int {
	check := false
	var paths []string
	for _, a := range args {
		if a == "--check" {
			check = true
			continue
		}
		paths = append(paths, a)
	}
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "easyvm walk FILE... - convert a walkthrough to the sentence form")
		return 2
	}

	failed := 0
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			failed++
			continue
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			failed++
			continue
		}
		out, err := walk.YAMLToWalk(doc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			failed++
			continue
		}
		if check {
			back, err := walk.WalkToYAML(out)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: the .walk does not parse: %v\n", path, err)
				failed++
				continue
			}
			if diff := docDiff(doc, back); diff != "" {
				fmt.Fprintf(os.Stderr, "%s: changed at %s\n", path, diff)
				failed++
				continue
			}
			fmt.Printf("%s: identical\n", path)
			continue
		}
		dest := strings.TrimSuffix(path, filepath.Ext(path)) + ".walk"
		if err := os.WriteFile(dest, []byte(out), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", dest, err)
			failed++
			continue
		}
		fmt.Printf("%s -> %s\n", path, dest)
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// docDiff is the first place two documents disagree, for the message.
func docDiff(want, got any) string { return diffPath(want, got, "") }

func diffPath(want, got any, at string) string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return at + ": not a mapping"
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok {
				return at + "." + k + ": missing"
			}
			if d := diffPath(wv, gv, at+"."+k); d != "" {
				return d
			}
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				return at + "." + k + ": unexpected"
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok || len(w) != len(g) {
			return at + ": lists differ"
		}
		for i := range w {
			if d := diffPath(w[i], g[i], fmt.Sprintf("%s.%d", at, i)); d != "" {
				return d
			}
		}
	default:
		if fmt.Sprint(want) != fmt.Sprint(got) {
			return fmt.Sprintf("%s: want %v, got %v", at, want, got)
		}
	}
	return ""
}
