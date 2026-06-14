package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
)

const (
	defaultPortRange = "60000-62999"
	midwayPortRange  = "5000-5999"
)

func defaultAddress() string {
	allAddress := getIPs()
	defaultGateway := getGateway()
	if len(allAddress) == 0 {
		return "127.0.0.1"
	}
	addr := allAddress[0]
	if defaultGateway != "" {
		if lastIndex := strings.LastIndex(defaultGateway, "."); lastIndex != -1 {
			pre := defaultGateway[:lastIndex]
			for _, a := range allAddress {
				if strings.HasPrefix(a, pre) {
					return a
				}
			}
		}
	}
	return addr
}

func defaultPortSpec() string {
	host, err := os.Hostname()
	if err == nil && strings.HasPrefix(host, "midway3") {
		return midwayPortRange
	}
	return defaultPortRange
}

func main() {
	var (
		address  string
		duration string
		portSpec string
		title    string
		token    string
		tbBin    string
		tbDir    string
	)
	defAddr := defaultAddress()
	defPort := defaultPortSpec()
	flag.StringVar(&address, "address", defAddr, "IP address to bind")
	flag.StringVar(&address, "a", defAddr, "IP address to bind (shorthand)")
	flag.StringVar(&duration, "duration", "7d", "server lifetime before automatic exit (e.g. 7d, 12h, 30m)")
	flag.StringVar(&portSpec, "port", defPort, "port or port range (e.g. 60000 or 60000-70000)")
	flag.StringVar(&portSpec, "p", defPort, "port or port range (shorthand)")
	flag.StringVar(&title, "title", "", "page title; defaults to full served folder path")
	flag.StringVar(&title, "t", "", "page title (shorthand)")
	flag.StringVar(&token, "token", "", "access token (>=8 chars); auto-generated if empty")
	flag.StringVar(&tbDir, "tb-dir", "", "TensorBoard logdir; launches & reverse-proxies an external tensorboard under /tensorboard/")
	flag.StringVar(&tbBin, "tb-bin", "", "path to the tensorboard binary (default: 'tensorboard' on PATH)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] [path]\n\nServes a file or directory over HTTP with token auth.\nIf no path is given, the current directory is served.\n\nFlags:\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	path := "."
	if flag.NArg() >= 1 {
		path = flag.Arg(0)
	}

	portLo, portHi, err := parsePortSpec(portSpec)
	if err != nil {
		log.Fatalf("invalid --port: %v", err)
	}
	lifetime, err := parseDurationSpec(duration)
	if err != nil {
		log.Fatalf("invalid --duration: %v", err)
	}

	if token == "" {
		t, err := generateRandomToken(16)
		if err != nil {
			log.Fatalf("could not generate security token: %v", err)
		}
		token = t
	} else if err := validateToken(token); err != nil {
		log.Fatalf("invalid --token: %v", err)
	}

	fileDir, fileBase, err := parsePath(path)
	if err != nil {
		log.Fatalf("%v", err)
	}
	displayRoot := defaultPageTitle(path, fileBase)
	if title == "" {
		title = displayRoot
	}

	if err := serveFiles(address, portLo, portHi, fileDir, fileBase, title, displayRoot, token, tbBin, tbDir, lifetime); err != nil {
		log.Fatalf("%v", err)
	}
}
