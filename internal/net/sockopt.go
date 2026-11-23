package net

import "os"

// osGetenv is `os.Getenv`, behind a name so the files that read the environment do not each import os,
// and so a reader can see every place this daemon reads one.
//
// THE FOUR SOCKET CONSTANTS AND THE GETSOCKOPT SYSCALL THAT USED TO BE HERE MOVED WITH THE TRANSPARENT
// PROXY, to github.com/easydev-org/easytun, which needs them for SO_ORIGINAL_DST.
func osGetenv(key string) string { return os.Getenv(key) }
