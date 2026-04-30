package main

import (
	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/shurlinet/shurli/tools/boundarycheck"
)

func main() {
	singlechecker.Main(boundarycheck.Analyzer)
}
