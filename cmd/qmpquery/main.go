// ASK QEMU WHAT INPUT DEVICES IT ACTUALLY HAS.
//
// `input-send-event` with no `device:` lets qemu pick, and what it picks is the question this
// answers. If the list has no absolute pointing device - or has one that is not the tablet the
// walkthrough expects - then every abs event is delivered to something that cannot honour it.
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
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	defer nc.Close()
	r := bufio.NewReader(nc)
	// greeting
	line, _ := r.ReadString('\n')
	_ = line
	send := func(v any) string {
		b, _ := json.Marshal(v)
		_, _ = nc.Write(append(b, '\n'))
		for {
			out, err := r.ReadString('\n')
			if err != nil {
				return ""
			}
			var probe map[string]any
			if json.Unmarshal([]byte(out), &probe) == nil {
				if _, ok := probe["event"]; ok {
					continue // an async event, not our reply
				}
			}
			return out
		}
	}
	send(map[string]any{"execute": "qmp_capabilities"})
	fmt.Println("input devices:")
	fmt.Println(send(map[string]any{"execute": "query-input-devices"}))
	fmt.Println("name:")
	fmt.Println(send(map[string]any{"execute": "query-name"}))
}
