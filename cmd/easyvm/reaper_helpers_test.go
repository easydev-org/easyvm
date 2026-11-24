package main

import "os/exec"

func osExecEcho() *exec.Cmd {
	cmd := exec.Command("true")
	_ = cmd.Start()
	return cmd
}
