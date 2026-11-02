package main

import (
	"github.com/KeepS13/HQIP/internal/winclient"
	"os"
)

func main() {
	if err := winclient.HelperMain(os.Args[1:]); err != nil {
		os.Exit(1)
	}
}
