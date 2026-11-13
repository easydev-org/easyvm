package main

import (
	"fmt"
	"os"

	"github.com/easydev-org/easyvm/internal/screen"
)

func main() {
	img, err := screen.Load(os.Args[1])
	if err != nil {
		fmt.Println("load:", err)
		return
	}
	ok, st := screen.IsWindowsDesktop(img)
	fmt.Printf("isDesktop=%v\n%s\n", ok, st.Explain())
}
