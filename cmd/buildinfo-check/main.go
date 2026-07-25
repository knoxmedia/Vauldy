package main

import (
	"flag"
	"fmt"
	"os"

	"knox-media/internal/buildinfo"
)

func main() {
	allowDirty := flag.Bool("allow-dirty", false, "allow dirty source state for an explicitly development artifact")
	flag.Parse()
	info := buildinfo.Current()
	if err := buildinfo.ValidateRelease(info, *allowDirty); err != nil {
		fmt.Fprintf(os.Stderr, "build metadata validation failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("build metadata valid: %s\n", info.String())
}
