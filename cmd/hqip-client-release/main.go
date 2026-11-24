// hqip-client-release signs installation metadata. The signing seed stays in a
// local protected directory; output contains only the public verification key.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/KeepS13/HQIP/internal/clientrelease"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	keyPath := flag.String("key", ".local/godot-signing/ed25519.seed", "private signing seed")
	artifact := flag.String("artifact", "", "package to sign; omit to initialize the key")
	platform := flag.String("platform", "windows-amd64", "target platform")
	version := flag.String("version", "5.0.0-preview.1", "release version")
	sequence := flag.Int64("sequence", 5000001, "monotonic release sequence")
	output := flag.String("output", "dist", "artifact directory")
	prune := flag.Bool("prune", false, "inspect unreferenced packages without reading the private key")
	apply := flag.Bool("apply", false, "delete verified prune candidates; requires -prune")
	publicKey := flag.String("public-key", "", "trusted Ed25519 public key for -prune")
	flag.Parse()
	if *prune {
		if *artifact != "" || *publicKey == "" {
			return fmt.Errorf("-prune requires -public-key and cannot publish an artifact")
		}
		return pruneAndReport(*output, *publicKey, *apply)
	}
	if *apply {
		return fmt.Errorf("-apply requires -prune")
	}
	seed, err := os.ReadFile(*keyPath)
	if os.IsNotExist(err) {
		if err = os.MkdirAll(filepath.Dir(*keyPath), 0700); err != nil {
			return err
		}
		seed = make([]byte, ed25519.SeedSize)
		if _, err = rand.Read(seed); err != nil {
			return err
		}
		file, err := os.OpenFile(*keyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = file.Write(seed)
		closed := file.Close()
		if err != nil {
			return err
		}
		if closed != nil {
			return closed
		}
	} else if err != nil {
		return err
	}
	if len(seed) != ed25519.SeedSize {
		return fmt.Errorf("invalid signing seed")
	}
	private := ed25519.NewKeyFromSeed(seed)
	public := hex.EncodeToString(private.Public().(ed25519.PublicKey))
	fmt.Println(public)
	if *artifact == "" {
		return nil
	}
	file, err := os.Open(*artifact)
	if err != nil {
		return err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	file.Close()
	if err != nil {
		return err
	}
	release := clientrelease.Release{Schema: 1, Version: *version, Sequence: *sequence, Platform: *platform, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size, Filename: clientrelease.Filename(*platform), APIMin: 3, APIMax: 3}
	envelope, err := clientrelease.Sign(release, private)
	if err != nil {
		return err
	}
	if err = publishArtifact(*artifact, *output, release); err != nil {
		return err
	}
	if err = publishCatalog(*output, public, envelope); err != nil {
		return err
	}
	// Only a fully published catalog may authorize removal of older packages.
	// Cleanup failure leaves the new catalog and every retained version intact.
	if err = pruneAndReport(*output, public, true); err != nil {
		return fmt.Errorf("catalog published, artifact cleanup failed: %w", err)
	}
	return nil
}
