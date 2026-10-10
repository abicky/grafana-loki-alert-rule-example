package main

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/cockroachdb/errors"
)

func connect() error {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:1", time.Second)
	if err != nil {
		return errors.Wrap(err, "failed to connect to 127.0.0.1:1")
	}
	defer conn.Close()
	return nil
}

func run() error {
	if err := connect(); err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Printf("%+v\n", errors.Formattable(err))
		os.Exit(1)
	}
}
