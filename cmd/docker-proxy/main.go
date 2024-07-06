package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/docker/docker/dockerversion"
	"github.com/ishidawataru/sctp"
)

// The caller is expected to pass-in open file descriptors ...
const (
	// Pipe for reporting status, as a string. "0\n" if the proxy
	// started normally. "1\n<error message>" otherwise.
	parentPipeFd uintptr = 3 + iota
	// Listening socket, ready to accept TCP connections or receive
	// UDP. Required for TCP/UDP. Not allowed for SCTP (the proxy
	// will open its own socket for SCTP, because it's not currently
	// possible to construct an sctp.SCTPListener from a file descriptor).
	listenSockFd
)

func main() {
	f := os.NewFile(parentPipeFd, "signal-parent")
	sockfd := os.NewFile(listenSockFd, "listen-sock")

	config := parseFlags()

	var (
		p   Proxy
		err error
	)

	switch config.Proto {
	case "tcp":
		if sockfd == nil {
			// TODO: fall back to HostIP:HostPort if no socket on fd 4, for compatibility with older daemons?
			log.Fatal("an existing open listen socket is required for tcp proxy")
		}
		l, err := net.FileListener(sockfd)
		if err != nil {
			log.Fatal(err)
		}
		listener, ok := l.(*net.TCPListener)
		if !ok {
			log.Fatalf("unexpected socket type for listener fd: %s", l.Addr().Network())
		}
		container := &net.TCPAddr{IP: config.ContainerIP, Port: config.ContainerPort}
		p, err = NewTCPProxy(listener, container)
	case "udp":
		if sockfd == nil {
			log.Fatal("an existing open listen socket is required for udp proxy")
		}
		l, err := net.FilePacketConn(sockfd)
		if err != nil {
			log.Fatal(err)
		}
		listener, ok := l.(*net.UDPConn)
		if !ok {
			log.Fatalf("unexpected socket type for listener fd: %s", l.LocalAddr().Network())
		}
		container := &net.UDPAddr{IP: config.ContainerIP, Port: config.ContainerPort}
		p, err = NewUDPProxy(listener, container)
	case "sctp":
		host := &sctp.SCTPAddr{IPAddrs: []net.IPAddr{{IP: config.HostIP}}, Port: config.HostPort}
		container := &sctp.SCTPAddr{IPAddrs: []net.IPAddr{{IP: config.ContainerIP}}, Port: config.ContainerPort}
		p, err = NewSCTPProxy(host, container)
	default:
		log.Fatalf("unsupported protocol %s", config.Proto)
	}

	sockfd.Close()

	if err != nil {
		fmt.Fprintf(f, "1\n%s", err)
		f.Close()
		os.Exit(1)
	}
	go handleStopSignals(p)
	fmt.Fprint(f, "0\n")
	f.Close()

	// Run will block until the proxy stops
	p.Run()
}

type ProxyConfig struct {
	Proto                   string
	HostIP, ContainerIP     net.IP
	HostPort, ContainerPort int
}

// parseFlags parses the flags passed on reexec to create the TCP/UDP/SCTP
// net.Addrs to map the host and container ports.
func parseFlags() ProxyConfig {
	var (
		config   ProxyConfig
		printVer bool
	)
	flag.StringVar(&config.Proto, "proto", "tcp", "proxy protocol")
	flag.TextVar(&config.HostIP, "host-ip", net.IPv4zero, "host ip")
	flag.IntVar(&config.HostPort, "host-port", -1, "host port")
	flag.TextVar(&config.ContainerIP, "container-ip", net.IPv4zero, "container ip")
	flag.IntVar(&config.ContainerPort, "container-port", -1, "container port")
	flag.BoolVar(&printVer, "v", false, "print version information and quit")
	flag.BoolVar(&printVer, "version", false, "print version information and quit")
	flag.Parse()

	if printVer {
		fmt.Printf("docker-proxy (commit %s) version %s\n", dockerversion.GitCommit, dockerversion.Version)
		os.Exit(0)
	}

	return config
}

func handleStopSignals(p Proxy) {
	s := make(chan os.Signal, 10)
	signal.Notify(s, os.Interrupt, syscall.SIGTERM)

	for range s {
		p.Close()

		os.Exit(0)
	}
}
