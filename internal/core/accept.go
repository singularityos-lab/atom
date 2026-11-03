package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/singularityos-lab/atom/internal/service"
	"github.com/singularityos-lab/atom/internal/socketact"
)

// acceptLoop serves an Accept=yes socket: every connection starts its own
// instance of the "prefix@.service" template, at most MaxConnections at once.
func (m *Manager) acceptLoop(name string, sock *socketact.Socket, ls []*socketact.Listener) {
	prefix := strings.TrimSuffix(sock.Service, ".service")
	sem := make(chan struct{}, sock.MaxConnections)
	var seq atomic.Uint64
	for _, l := range ls {
		if l.Net == nil {
			continue
		}
		go func(ln net.Listener) {
			for {
				c, err := ln.Accept()
				if err != nil {
					if errors.Is(err, net.ErrClosed) {
						return
					}
					time.Sleep(100 * time.Millisecond)
					continue
				}
				inst := fmt.Sprintf("%s@%d.service", prefix, seq.Add(1))
				sem <- struct{}{}
				go func() {
					defer func() { <-sem }()
					m.serveConnection(name, inst, c)
				}()
			}
		}(l.Net)
	}
}

// serveConnection runs one template instance with the connection as its
// standard streams, and returns once that instance is no longer running.
func (m *Manager) serveConnection(sockName, inst string, c net.Conn) {
	defer c.Close()
	fc, ok := c.(interface{ File() (*os.File, error) })
	if !ok {
		return
	}
	f, err := fc.File()
	if err != nil {
		m.unitError(inst, err)
		return
	}
	defer f.Close()

	file, err := m.loader.Load(inst)
	if err != nil {
		m.unitError(inst, err)
		return
	}
	if file.Path == "" {
		m.unitError(inst, fmt.Errorf("%s: no template unit for %s", sockName, inst))
		return
	}
	cfg, err := service.ConfigFromFile(file)
	if err != nil {
		m.unitError(inst, err)
		return
	}
	svc := service.New(cfg)
	if v, _ := file.Get("Service", "StandardInput"); strings.TrimSpace(v) == "socket" {
		svc.Stdin = f
	}
	if v, _ := file.Get("Service", "StandardOutput"); strings.TrimSpace(v) == "socket" {
		svc.Stdout = f
	}
	m.mu.RLock()
	reg := m.logs
	m.mu.RUnlock()
	if reg != nil {
		w := reg.Writer(sockName)
		svc.Stderr = w
		if svc.Stdout == nil {
			svc.Stdout = w
		}
	}
	if err := svc.Start(context.Background()); err != nil {
		m.unitError(inst, err)
		return
	}
	for {
		switch svc.State() {
		case service.Active, service.Activating, service.Deactivating:
			time.Sleep(200 * time.Millisecond)
			continue
		}
		return
	}
}

func (m *Manager) unitError(name string, err error) {
	m.mu.RLock()
	hook := m.OnUnitError
	m.mu.RUnlock()
	if hook != nil {
		hook(name, err)
	}
}
