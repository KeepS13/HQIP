// This build-only command embeds and verifies Windows icon resource ID 3,
// the resource Wails v3.1.0 uses for its native windows.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/tc-hib/winres"
)

func main() {
	iconPath := flag.String("icon", "", "source ICO containing every supported size")
	out := flag.String("out", "", "new Windows amd64 COFF resource file")
	verify := flag.String("verify", "", "built executable whose icon resources must match")
	flag.Parse()
	if *iconPath == "" || (*out == "") == (*verify == "") {
		log.Fatal("provide -icon and exactly one of -out or -verify")
	}
	iconFile, err := os.Open(*iconPath)
	if err != nil {
		log.Fatal(err)
	}
	defer iconFile.Close()
	icon, err := winres.LoadICO(iconFile)
	if err != nil {
		log.Fatal(err)
	}
	resources := winres.ResourceSet{}
	if err = resources.SetIcon(winres.ID(3), icon); err != nil {
		log.Fatal(err)
	}
	if *verify != "" {
		if err = verifyIcon(*verify, &resources); err != nil {
			log.Fatal(err)
		}
		fmt.Println("Verified Windows PE icon group ID 3 and all image resources against source ICO.")
		return
	}
	// Do not replace another build's intermediate resource object.
	output, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		log.Fatal(err)
	}
	err = resources.WriteObject(output, winres.ArchAMD64)
	closeErr := output.Close()
	if err != nil {
		log.Fatal(err)
	}
	if closeErr != nil {
		log.Fatal(closeErr)
	}
}

func verifyIcon(path string, expected *winres.ResourceSet) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	actual, err := winres.LoadFromEXE(file)
	if err != nil {
		return err
	}
	var mismatch error
	expected.Walk(func(typeID, resourceID winres.Identifier, language uint16, data []byte) bool {
		if !bytes.Equal(actual.Get(typeID, resourceID, language), data) {
			mismatch = fmt.Errorf("Windows PE icon resource mismatch: type %v, ID %v", typeID, resourceID)
			return false
		}
		return true
	})
	return mismatch
}
