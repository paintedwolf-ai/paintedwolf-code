package main

import (
	"encoding/json"
	"io"

	"github.com/lycaon/lycaon/internal/credentialstore"
)

func describeCustody(stdout io.Writer, vaultPath string) error {
	report, err := credentialstore.DescribeCustody(vaultPath)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(report)
}
