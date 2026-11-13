package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	nc, err := net.DialTimeout("unix", os.Args[1], 5*time.Second)
	if err != nil { fmt.Println("dial:", err); os.Exit(1) }
	defer nc.Close()
	r := bufio.NewReader(nc)
	r.ReadString('\n')
	send := func(v any) string {
		b, _ := json.Marshal(v)
		nc.Write(append(b, '\n'))
		for {
			out, err := r.ReadString('\n')
			if err != nil { return "" }
			var probe map[string]any
			if json.Unmarshal([]byte(out), &probe) == nil {
				if _, ok := probe["event"]; ok { continue }
			}
			return out
		}
	}
	send(map[string]any{"execute": "qmp_capabilities"})
	// PNG format, which qemu 9+ supports for screendump.
	fmt.Println("png:", send(map[string]any{
		"execute": "screendump",
		"arguments": map[string]any{"filename": "/tmp/frame-test.png", "format": "png"},
	}))
}
