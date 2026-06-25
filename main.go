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
	if len(os.Args) >= 2 && os.Args[1] == cleanupWatcherCommand {
		os.Exit(runCleanupWatcher(os.Args[2:]))
	}

	var (
		address       string
		duration      string
		localPortSpec string
		portSpec      string
		socketPath    string
		sshHost       string
		title         string
		token         string
		tbBin         string
		tbDir         string
		useSSH        bool
	)
	defAddr := defaultAddress()
	defPort := defaultPortSpec()
	flag.StringVar(&address, "address", defAddr, "IP address to bind")
	flag.StringVar(&address, "a", defAddr, "IP address to bind (shorthand)")
	flag.StringVar(&duration, "duration", "7d", "server lifetime before automatic exit (e.g. 7d, 12h, 30m)")
	flag.StringVar(&localPortSpec, "local-port", defPort, "local browser port or port range for --ssh (e.g. 60123 or 60000-60100)")
	flag.StringVar(&portSpec, "port", defPort, "port or port range (e.g. 60000 or 60000-70000)")
	flag.StringVar(&portSpec, "p", defPort, "port or port range (shorthand)")
	flag.BoolVar(&useSSH, "ssh", false, "listen on a private Unix socket and print an SSH tunnel command")
	flag.StringVar(&sshHost, "ssh-host", "", "SSH target printed by --ssh (default: USER@hostname)")
	flag.StringVar(&socketPath, "socket", "", "Unix socket path for --ssh; parent directory must be private")
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
	explicitFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) {
		explicitFlags[f.Name] = true
	})

	path := "."
	if flag.NArg() >= 1 {
		path = flag.Arg(0)
	}

	if err := validateTransportFlags(useSSH, explicitFlags); err != nil {
		log.Fatalf("%v", err)
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

	if useSSH {
		localPortLo, localPortHi, err := parsePortSpec(localPortSpec)
		if err != nil {
			log.Fatalf("invalid --local-port: %v", err)
		}
		localPort, err := pickPort(localPortLo, localPortHi)
		if err != nil {
			log.Fatalf("could not choose --local-port: %v", err)
		}
		if sshHost == "" {
			sshHost = defaultSSHHost()
		}
		err = serveFilesWithOptions(serveOptions{
			Mode:         serveModeSSH,
			FileDir:      fileDir,
			FileBase:     fileBase,
			Title:        title,
			DisplayRoot:  displayRoot,
			Token:        token,
			TBBin:        tbBin,
			TBDir:        tbDir,
			Duration:     lifetime,
			LocalPort:    localPort,
			SocketPath:   socketPath,
			SSHHost:      sshHost,
			StartWatcher: true,
		})
		if err != nil {
			log.Fatalf("%v", err)
		}
		return
	}

	portLo, portHi, err := parsePortSpec(portSpec)
	if err != nil {
		log.Fatalf("invalid --port: %v", err)
	}
	if err := serveFiles(address, portLo, portHi, fileDir, fileBase, title, displayRoot, token, tbBin, tbDir, lifetime); err != nil {
		log.Fatalf("%v", err)
	}
}
