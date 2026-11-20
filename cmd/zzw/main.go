package main

import (
	"fmt"
	"os"

	"github.com/easydev-org/easyvm/internal/screen"
)

func main() {
	ref, err := screen.Load(os.Args[1])
	if err != nil { fmt.Println("ref:", err); return }
	rc, err := screen.Crop(ref, [4]int{0, 0, 1920, 120}, 1)
	if err != nil { fmt.Println("crop:", err); return }
	for _, p := range os.Args[2:] {
		img, err := screen.Load(p)
		if err != nil { fmt.Printf("%-20s LOAD ERR\n", p); continue }
		c, err := screen.Crop(img, [4]int{0, 0, 1920, 120}, 1)
		if err != nil { fmt.Printf("%-20s CROP: %v\n", p, err); continue }
		d, _ := screen.Compare(rc, c, 8)
		fmt.Printf("%-20s similarity=%.4f\n", p, 1-d.Changed)
	}
}
