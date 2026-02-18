package main

import (
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Config for the mock server.
type Config struct {
	RPC      RPCConfig      `yaml:"rpc"`
	Snapshot SnapshotConfig `yaml:"snapshot"`
}

type RPCConfig struct {
	ListenAddr string `yaml:"listen_addr"`
	Identity   string `yaml:"identity"`
	Slot       uint64 `yaml:"slot"`
	Nodes      []Node `yaml:"nodes"`
}

type Node struct {
	Pubkey  string `yaml:"pubkey"`
	Gossip  string `yaml:"gossip"`
	RPC     string `yaml:"rpc"`
	Version string `yaml:"version"`
}

type SnapshotConfig struct {
	ListenAddr string `yaml:"listen_addr"`
	FullSlot   uint64 `yaml:"full_slot"`
	FullHash   string `yaml:"full_hash"`
	FullSizeMB int    `yaml:"full_size_mb"`
	IncSlot    uint64 `yaml:"incremental_slot"`
	IncHash    string `yaml:"incremental_hash"`
	IncSizeMB  int    `yaml:"incremental_size_mb"`
}

func main() {
	configPath := flag.String("config", "mock-server/config.yaml", "path to mock server config")
	flag.Parse()

	data, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("reading config: %v", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("parsing config: %v", err)
	}

	var wg sync.WaitGroup

	// Start RPC server
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Printf("RPC server listening on %s (identity=%s, slot=%d, nodes=%d)",
			cfg.RPC.ListenAddr, cfg.RPC.Identity, cfg.RPC.Slot, len(cfg.RPC.Nodes))
		if err := http.ListenAndServe(cfg.RPC.ListenAddr, rpcHandler(cfg.RPC)); err != nil {
			log.Fatalf("RPC server: %v", err)
		}
	}()

	// Start snapshot server
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Printf("Snapshot server listening on %s (full_slot=%d size=%dMB, inc_slot=%d size=%dMB)",
			cfg.Snapshot.ListenAddr, cfg.Snapshot.FullSlot, cfg.Snapshot.FullSizeMB,
			cfg.Snapshot.IncSlot, cfg.Snapshot.IncSizeMB)
		if err := http.ListenAndServe(cfg.Snapshot.ListenAddr, snapshotHandler(cfg.Snapshot)); err != nil {
			log.Fatalf("Snapshot server: %v", err)
		}
	}()

	wg.Wait()
}

func rpcHandler(cfg RPCConfig) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			JSONRPC string `json:"jsonrpc"`
			ID      int    `json:"id"`
			Method  string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		var result any
		switch req.Method {
		case "getIdentity":
			result = map[string]string{"identity": cfg.Identity}
		case "getSlot":
			result = cfg.Slot
		case "getClusterNodes":
			var nodes []map[string]any
			for _, n := range cfg.Nodes {
				node := map[string]any{
					"pubkey":  n.Pubkey,
					"gossip":  n.Gossip,
					"version": n.Version,
				}
				if n.RPC != "" {
					node["rpc"] = n.RPC
				}
				nodes = append(nodes, node)
			}
			result = nodes
		default:
			writeRPCError(w, req.ID, -32601, "method not found")
			return
		}

		resultJSON, _ := json.Marshal(result)
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  json.RawMessage(resultJSON),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		log.Printf("RPC: %s → OK", req.Method)
	})
	return mux
}

func writeRPCError(w http.ResponseWriter, id int, code int, msg string) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": msg},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func snapshotHandler(cfg SnapshotConfig) http.Handler {
	fullFilename := fmt.Sprintf("snapshot-%d-%s.tar.zst", cfg.FullSlot, cfg.FullHash)
	fullSize := int64(cfg.FullSizeMB) * 1024 * 1024

	incFilename := ""
	var incSize int64
	if cfg.IncSlot > 0 {
		incFilename = fmt.Sprintf("incremental-snapshot-%d-%d-%s.tar.zst", cfg.FullSlot, cfg.IncSlot, cfg.IncHash)
		incSize = int64(cfg.IncSizeMB) * 1024 * 1024
	}

	mux := http.NewServeMux()

	// HEAD /snapshot.tar.bz2 → 302 redirect
	mux.HandleFunc("/snapshot.tar.bz2", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			log.Printf("Snapshot: HEAD /snapshot.tar.bz2 → 302 %s", fullFilename)
			w.Header().Set("Location", "/"+fullFilename)
			w.WriteHeader(http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})

	// HEAD /incremental-snapshot.tar.bz2 → 302 redirect
	if incFilename != "" {
		mux.HandleFunc("/incremental-snapshot.tar.bz2", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				log.Printf("Snapshot: HEAD /incremental-snapshot.tar.bz2 → 302 %s", incFilename)
				w.Header().Set("Location", "/"+incFilename)
				w.WriteHeader(http.StatusFound)
				return
			}
			http.NotFound(w, r)
		})
	}

	// GET/HEAD for actual snapshot files (with Range support)
	mux.HandleFunc("/"+fullFilename, func(w http.ResponseWriter, r *http.Request) {
		serveRandomData(w, r, fullFilename, fullSize)
	})
	if incFilename != "" {
		mux.HandleFunc("/"+incFilename, func(w http.ResponseWriter, r *http.Request) {
			serveRandomData(w, r, incFilename, incSize)
		})
	}

	// Catch-all
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Snapshot: 404 %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})

	return mux
}

func serveRandomData(w http.ResponseWriter, r *http.Request, filename string, totalSize int64) {
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", strconv.FormatInt(totalSize, 10))
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
		log.Printf("Snapshot: HEAD /%s → 200 (size=%d)", filename, totalSize)
		return
	}

	// Handle Range requests
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" {
		rangeHeader = strings.TrimPrefix(rangeHeader, "bytes=")
		parts := strings.Split(rangeHeader, "-")
		start, _ := strconv.ParseInt(parts[0], 10, 64)
		end := totalSize - 1
		if len(parts) > 1 && parts[1] != "" {
			end, _ = strconv.ParseInt(parts[1], 10, 64)
		}
		if end >= totalSize {
			end = totalSize - 1
		}
		length := end - start + 1

		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, totalSize))
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		w.WriteHeader(http.StatusPartialContent)
		log.Printf("Snapshot: GET /%s Range=%s → 206 (%d bytes)", filename, rangeHeader, length)
		streamRandomBytes(w, length)
		return
	}

	// Full download
	w.Header().Set("Content-Length", strconv.FormatInt(totalSize, 10))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusOK)
	log.Printf("Snapshot: GET /%s → 200 (size=%d)", filename, totalSize)
	streamRandomBytes(w, totalSize)
}

func streamRandomBytes(w http.ResponseWriter, total int64) {
	buf := make([]byte, 256*1024) // 256KB chunks
	var written int64
	for written < total {
		remaining := total - written
		if remaining < int64(len(buf)) {
			buf = buf[:remaining]
		}
		rand.Read(buf)
		n, err := w.Write(buf)
		if err != nil {
			return // client disconnected
		}
		written += int64(n)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}
