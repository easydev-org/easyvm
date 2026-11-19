package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/easydev-org/easyvm/internal/screen"
)

func main() {
	// zzv <ref> <x1> <y1> <x2> <y2> <frames...>
	x1, _ := strconv.Atoi(os.Args[2]); y1, _ := strconv.Atoi(os.Args[3])
	x2, _ := strconv.Atoi(os.Args[4]); y2, _ := strconv.Atoi(os.Args[5])
	ref, err := screen.Load(os.Args[1])
	if err != nil { fmt.Println(err); return }
	rc, err := screen.Crop(ref, [4]int{x1, y1, x2, y2}, 1)
	if err != nil { fmt.Println("crop ref:", err); return }
	for _, p := range os.Args[6:] {
		img, err := screen.Load(p)
		if err != nil { continue }
		c, err := screen.Crop(img, [4]int{x1, y1, x2, y2}, 1)
		if err != nil { continue }
		d, _ := screen.Compare(rc, c, 8)
		fmt.Printf("%-14s %.4f\n", p, 1-d.Changed)
	}
}
