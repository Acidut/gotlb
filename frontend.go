package main

import (
	"log"
	"net"
	"sync"

	"github.com/rcrowley/go-metrics"
)

// NewFrontend creates a new Frontend instance with appId, frontend
// and array of backends.
func NewFrontend(appId, port string, backends []string) *Frontend {
	return &Frontend{
		appId:    appId,
		backends: backends,
		port:     port,
		strategy: RoundRobinStrategy(), // TODO - Make this configurable from labels
	}
}

// Frontend represents a instance for an app with a set of backends
type Frontend struct {
	appId    string
	lock     sync.Mutex
	backends []string
	port     string
	listener net.Listener
	strategy LoadBalancingStrategy
}

func (f *Frontend) Lookup() string {
	return f.strategy.Next()
}

func (f *Frontend) AddBackend(backend string) {
	f.lock.Lock()
	defer f.lock.Unlock()
	f.backends = append(f.backends, backend)
	f.strategy.AddBackend(backend)
}

func (f *Frontend) RemoveBackend(backend string) {
	f.lock.Lock()
	defer f.lock.Unlock()
	idx, found := f.findIdxOfBackend(backend)
	if found {
		f.backends = append(f.backends[:idx], f.backends[idx+1:]...)
	} else {
		log.Printf("[WARN] Backend %s is not part of this frontend - %s\n", backend, f.appId)
	}
	f.strategy.RemoveBackend(backend)
}

func (f *Frontend) LenOfBackends() int {
	f.lock.Lock()
	defer f.lock.Unlock()
	return len(f.backends)
}

func (f *Frontend) findIdxOfBackend(backend string) (int, bool) {
	for idx, node := range f.backends {
		if node == backend {
			return idx, true
		}
	}

	return -1, false
}

// Start listening on the frontend and start routing requests to backends
func (f *Frontend) Start() {
	log.Printf("Starting Frontend for %s via %s\n", f.appId, f.port)
	l, err := net.Listen("tcp", ":"+f.port)
	f.listener = l
	log.Printf("Started Frontend for %s at %s\n", f.appId, f.port)
	if err != nil {
		log.Fatal(err)
	}

	for {
		// Wait for a connection.
		conn, err := l.Accept()
		if err != nil {
			log.Fatal(err)
		}

		metrics.GetOrRegisterCounter("frontend-requests", MetricsRegistry).Inc(int64(1))
		// Handle the connection in a new goroutine.
		// The loop then returns to accepting, so that
		// multiple connections may be served concurrently.
		go NewRequest(conn, f.Lookup(), f.appId)
	}
}

func (f *Frontend) Stop() {
	log.Println("[INFO] Stopping the frontend - " + f.appId)
	if f.listener != nil {
		err := f.listener.Close()
		if err != nil {
			log.Printf("[ERR] Error occured while closing the Frontend - %v\n", err)
		}
	}
	log.Println("[INFO] Stopped the frontend - " + f.appId)
}
