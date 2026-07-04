package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultAddr    = ":2222"
	defaultEnable  = "/etc/atom/probe.enabled" // baked read-only marker: dev/managed policy allows it
	devMarker      = "/etc/atom/dev.enabled"   // the shared dev-image marker also permits it
	certValidYears = 10
)

// probeDir is where the token and TLS material live. It is per-user so the agent
// runs as the invoking user with no root and no writes to /etc (the dormant,
// opt-in model): the gate marker in /etc is read-only, everything writable is here.
func probeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, ".atom-probe")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("atom-probe-%d", os.Getuid()))
}

func defTokenPath() string { return filepath.Join(probeDir(), "token") }
func defCertPath() string  { return filepath.Join(probeDir(), "probe.crt") }
func defKeyPath() string   { return filepath.Join(probeDir(), "probe.key") }

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "gen-token":
		return genToken(args[1:])
	case "serve":
		return serve(args[1:])
	case "connect":
		return connect(args[1:])
	default:
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: atom-probe <gen-token|serve|connect> [flags]")
}

func genToken(args []string) int {
	fs := flag.NewFlagSet("gen-token", flag.ContinueOnError)
	path := fs.String("token-file", defTokenPath(), "where to write the token")
	printTok := fs.Bool("print", false, "also print the token value (to surface it at first boot)")
	keepExisting := fs.Bool("keep-existing", false, "do nothing if the token file already exists")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *keepExisting {
		if b, err := os.ReadFile(*path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
			if *printTok {
				fmt.Printf("PROBE TOKEN: %s\n", strings.TrimSpace(string(b)))
			}
			return 0
		}
	}
	_ = os.MkdirAll(filepath.Dir(*path), 0o700)
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		fmt.Fprintln(os.Stderr, "atom-probe:", err)
		return 1
	}
	tok := hex.EncodeToString(buf)
	if err := os.WriteFile(*path, []byte(tok+"\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "atom-probe:", err)
		return 1
	}
	fmt.Printf("wrote token to %s (mode 0600). Share it with the client out of band.\n", *path)
	if *printTok {
		fmt.Printf("PROBE TOKEN: %s\n", tok)
	}
	return 0
}

func serve(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", defaultAddr, "listen address (LAN)")
	certPath := fs.String("cert", defCertPath(), "TLS cert (generated if absent)")
	keyPath := fs.String("key", defKeyPath(), "TLS key (generated if absent)")
	tokenPath := fs.String("token-file", defTokenPath(), "shared auth token file")
	enable := fs.String("enable-marker", defaultEnable, "policy marker that permits serving")
	dev := fs.Bool("dev", false, "permit serving without the enable marker (dev builds)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Policy gate: never run on a user image. Requires an explicit enable marker
	// (managed) or --dev (dev build), AND a token.
	if !*dev {
		_, e1 := os.Stat(*enable)
		_, e2 := os.Stat(devMarker)
		if e1 != nil && e2 != nil {
			fmt.Fprintf(os.Stderr, "atom-probe: refusing to serve: no %s, no %s, and no --dev (policy gate)\n", *enable, devMarker)
			return 1
		}
	}
	token, err := os.ReadFile(*tokenPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "atom-probe: refusing to serve: no token at %s (run gen-token)\n", *tokenPath)
		return 1
	}
	tok := strings.TrimSpace(string(token))
	if tok == "" {
		fmt.Fprintln(os.Stderr, "atom-probe: refusing to serve: empty token")
		return 1
	}

	cert, fp, err := loadOrGenCert(*certPath, *keyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "atom-probe:", err)
		return 1
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS12}
	ln, err := tls.Listen("tcp", *addr, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "atom-probe:", err)
		return 1
	}
	fmt.Printf("atom-probe: serving on %s\natom-probe: cert fingerprint (pin this on the client): %s\n", *addr, fp)
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go handleConn(conn, tok)
	}
}

func handleConn(conn net.Conn, token string) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	br := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "atom-probe: %s: no token, closing\n", remote)
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(line)), []byte(token)) != 1 {
		fmt.Fprintf(os.Stderr, "atom-probe: %s: AUTH FAILED\n", remote)
		return
	}
	_ = conn.SetReadDeadline(time.Time{}) // clear: interactive from here
	fmt.Fprintf(os.Stderr, "atom-probe: %s: authenticated, opening shell\n", remote)

	cmd := exec.Command("/bin/sh")
	cmd.Env = append(os.Environ(), "TERM=dumb")
	cmd.Stdin = br // includes any bytes buffered past the token line
	cmd.Stdout = conn
	cmd.Stderr = conn
	_ = cmd.Run()
	fmt.Fprintf(os.Stderr, "atom-probe: %s: shell closed\n", remote)
}

func connect(args []string) int {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fingerprint := fs.String("fingerprint", "", "expected server cert SHA256 (required unless --insecure)")
	tokenPath := fs.String("token-file", defTokenPath(), "shared auth token file")
	insecure := fs.Bool("insecure", false, "skip cert pinning (NOT recommended)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "atom-probe connect: needs HOST:PORT")
		return 2
	}
	target := fs.Arg(0)
	// Allow flags on either side of the host: re-parse whatever followed it.
	if err := fs.Parse(fs.Args()[1:]); err != nil {
		return 2
	}
	if *fingerprint == "" && !*insecure {
		fmt.Fprintln(os.Stderr, "atom-probe connect: --fingerprint required (or --insecure)")
		return 2
	}
	token, err := os.ReadFile(*tokenPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "atom-probe:", err)
		return 1
	}
	want := strings.ToLower(strings.ReplaceAll(*fingerprint, ":", ""))
	cfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if *insecure {
				return nil
			}
			if len(raw) == 0 {
				return fmt.Errorf("no server cert")
			}
			sum := sha256.Sum256(raw[0])
			if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(want)) != 1 {
				return fmt.Errorf("server cert fingerprint mismatch (possible MITM)")
			}
			return nil
		}}
	conn, err := tls.Dial("tcp", target, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "atom-probe:", err)
		return 1
	}
	defer conn.Close()
	fmt.Fprintf(conn, "%s\n", strings.TrimSpace(string(token)))
	go func() {
		io.Copy(conn, os.Stdin)
		// stdin ended (pipe EOF or Ctrl-D): signal it to the server so its shell
		// sees EOF and exits, instead of the session hanging open.
		conn.CloseWrite()
	}()
	io.Copy(os.Stdout, conn) // returns when the server closes (shell exited)
	return 0
}

// loadOrGenCert loads the TLS cert/key, generating a persisted self-signed
// ed25519 pair on first run. Returns the cert and its SHA256 fingerprint (hex).
func loadOrGenCert(certPath, keyPath string) (*tls.Certificate, string, error) {
	if _, err := os.Stat(certPath); err == nil {
		c, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, "", err
		}
		sum := sha256.Sum256(c.Certificate[0])
		return &c, hex.EncodeToString(sum[:]), nil
	}
	_ = os.MkdirAll(filepath.Dir(certPath), 0o700)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "atom-probe"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(certValidYears, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, "", err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, "", err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, "", err
	}
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(der)
	return &c, hex.EncodeToString(sum[:]), nil
}
