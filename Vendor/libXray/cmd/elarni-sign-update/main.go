// elarni-sign-update signs a release's SHA256SUMS with the release key, so installed builds
// accept its packages as an update (docs/RELEASING.md). It runs on the release machine only.
//
//	go run ./cmd/elarni-sign-update -generate KEYFILE       a new key; prints its public half
//	go run ./cmd/elarni-sign-update -key KEYFILE SHA256SUMS  writes SHA256SUMS.sig beside it
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/xtls/libxray/update"
)

func main() {
	generate := flag.String("generate", "", "write a new private key to this file")
	keyFile := flag.String("key", "", "the private key file")
	flag.Parse()
	if err := run(*generate, *keyFile, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "elarni-sign-update:", err)
		os.Exit(1)
	}
}

func run(generate, keyFile string, files []string) error {
	if generate != "" {
		if keyFile != "" || len(files) != 0 {
			return fmt.Errorf("-generate takes only the key file")
		}
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		// O_EXCL: a key that releases were signed with is never overwritten.
		file, err := os.OpenFile(generate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := file.WriteString(base64.StdEncoding.EncodeToString(private.Seed()) + "\n"); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		fmt.Println(base64.StdEncoding.EncodeToString(public))
		return nil
	}
	if keyFile == "" || len(files) != 1 {
		return fmt.Errorf("usage: elarni-sign-update -key KEYFILE SHA256SUMS")
	}
	text, err := os.ReadFile(keyFile)
	if err != nil {
		return err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(text)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return fmt.Errorf("%s is not a release key", keyFile)
	}
	sums, err := os.ReadFile(files[0])
	if err != nil {
		return err
	}
	private := ed25519.NewKeyFromSeed(seed)
	signature := update.Sign(private, sums)
	// A release signed with a key the installed builds do not know would update nobody.
	if !update.Signed(sums, signature) {
		return fmt.Errorf("%s is not a key this build trusts (update/update.go)", keyFile)
	}
	if err := os.WriteFile(files[0]+".sig", signature, 0o644); err != nil {
		return err
	}
	fmt.Println(base64.StdEncoding.EncodeToString(private.Public().(ed25519.PublicKey)))
	return nil
}
