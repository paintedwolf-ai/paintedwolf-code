package designkit

import (
	"embed"
)

//go:embed fonts/*.woff2 licenses/*.txt shell.css provenance.yaml
var embedded embed.FS
