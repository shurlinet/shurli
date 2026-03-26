package main

import (
	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/shurlinet/shurli/tools/importcheck"
)

func main() {
	singlechecker.Main(importcheck.Analyzer)
}
