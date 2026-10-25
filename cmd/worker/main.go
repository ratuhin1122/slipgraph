package main

import (
	"github.com/slipgraph/slipgraph/internal/platform/service"
	"os"
)

func main() { os.Exit(service.Run("worker", ":8081")) }
