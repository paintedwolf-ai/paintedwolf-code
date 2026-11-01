package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/modelstore"
)

func sign(ctx context.Context, args []string, output, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("model-bundle sign", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	directory := flags.String("bundle", "", "exported payload directory")
	manifestPath := flags.String("manifest", "", "unsigned export manifest")
	keys := flags.String("authority", "", "trusted release-key registry")
	keyID := flags.String("key-id", "", "release-key identity")
	keyPath := flags.String("private-key", "", "PEM PKCS#8 Ed25519 private key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *directory == "" || *manifestPath == "" || *keys == "" || *keyID == "" || *keyPath == "" {
		return errors.New("bundle, manifest, authority, key-id and private-key are required")
	}
	raw, err := readInput(*manifestPath, 128<<10)
	if err != nil {
		return err
	}
	var manifest modelstore.Manifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing export manifest content")
	}
	raw, err = readInput(*keys, modelstore.MaxAuthorityBytes)
	if err != nil {
		return err
	}
	authority, err := modelstore.DecodeAuthority(raw)
	if err != nil {
		return err
	}
	raw, err = readInput(*keyPath, 16<<10)
	if err != nil {
		return err
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return errors.New("private key must be one PEM PKCS#8 block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return errors.New("invalid PKCS#8 private key")
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return errors.New("release signing requires Ed25519")
	}
	signed, err := modelstore.SealBundle(ctx, *directory, manifest, authority, *keyID, private)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(signed)
}

func readInput(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("release input must be a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("release input exceeds its bound")
	}
	return raw, nil
}
