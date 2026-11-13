package main

import (
	"fmt"
	"os"
	"time"

	"github.com/easydev-org/easyvm/internal/qmp"
)

func main() {
	c, err := qmp.Open(os.Args[1], 10*time.Second)
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	defer c.Close()
	// Full-scale corner: if the axis is honoured, the pointer goes to the bottom-right.
	if err := c.Click(1919, 1079); err != nil {
		fmt.Println("click:", err)
		os.Exit(1)
	}
	fmt.Println("clicked bottom-right corner")
}
