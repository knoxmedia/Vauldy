//go:build !windows

package main

import "os"

func readAtomicTarget(path string) ([]byte, error) { return os.ReadFile(path) }
