package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/easydev-org/easyvm/internal/walk"
)

func main() {
	top := map[string]int{}
	deep := map[string]int{}
	n := 0
	filepath.Walk(".", func(p string, i os.FileInfo, e error) error {
		if e != nil || i.IsDir() || !strings.HasSuffix(p, ".walk") || strings.Contains(p, "/.git/") {
			return nil
		}
		f, err := walk.Load(p, nil)
		if err != nil {
			return nil
		}
		for _, s := range f.Steps {
			n++
			for k, v := range s.Spec {
				top[k]++
				walkDeep(v, deep)
			}
		}
		return nil
	})
	fmt.Printf("steps=%d  distinct top-level keys=%d\n\n", n, len(top))
	var ks []string
	for k := range top {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(a, b int) bool { return top[ks[a]] > top[ks[b]] })
	for _, k := range ks {
		fmt.Printf("%-20s %5d\n", k, top[k])
	}
}

func walkDeep(v any, c map[string]int) {
	switch t := v.(type) {
	case map[string]any:
		for k, vv := range t {
			c[k]++
			walkDeep(vv, c)
		}
	case []any:
		for _, it := range t {
			walkDeep(it, c)
		}
	}
}
