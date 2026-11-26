//go:build !unix

package main

import "time"

func processCPU() time.Duration { return 0 }
