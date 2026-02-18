package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s
}

func rpcHandler(t *testing.T, responses map[string]any) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		result, ok := responses[req.Method]
		if !ok {
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error":   map[string]any{"code": -32601, "message": "method not found"},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		resultJSON, _ := json.Marshal(result)
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  json.RawMessage(resultJSON),
		}
		json.NewEncoder(w).Encode(resp)
	}
}

func TestGetIdentity(t *testing.T) {
	server := newTestServer(t, rpcHandler(t, map[string]any{
		"getIdentity": map[string]string{"identity": "TestValidatorPubkey123"},
	}))

	client := NewClient(server.URL)
	identity, err := client.GetIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if identity != "TestValidatorPubkey123" {
		t.Errorf("expected TestValidatorPubkey123, got %q", identity)
	}
}

func TestGetSlot(t *testing.T) {
	server := newTestServer(t, rpcHandler(t, map[string]any{
		"getSlot": 135501350,
	}))

	client := NewClient(server.URL)
	slot, err := client.GetSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if slot != 135501350 {
		t.Errorf("expected 135501350, got %d", slot)
	}
}

func TestGetClusterNodes(t *testing.T) {
	rpcAddr := "10.0.0.1:8899"
	version := "2.2.4"
	server := newTestServer(t, rpcHandler(t, map[string]any{
		"getClusterNodes": []map[string]any{
			{
				"pubkey":  "NodePubkey1",
				"gossip":  "10.0.0.1:8001",
				"rpc":     rpcAddr,
				"version": version,
			},
			{
				"pubkey": "NodePubkey2",
				"gossip": "10.0.0.2:8001",
				"rpc":    nil,
			},
		},
	}))

	client := NewClient(server.URL)
	nodes, err := client.GetClusterNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
	if nodes[0].Pubkey != "NodePubkey1" {
		t.Errorf("expected NodePubkey1, got %q", nodes[0].Pubkey)
	}
	if nodes[0].RPC == nil || *nodes[0].RPC != rpcAddr {
		t.Errorf("expected RPC=%q, got %v", rpcAddr, nodes[0].RPC)
	}
	if nodes[1].RPC != nil {
		t.Errorf("expected nil RPC for node 2, got %v", nodes[1].RPC)
	}
}

func TestGetIdentity_RPCError(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"invalid request"}}`
		w.Write([]byte(resp))
	})

	client := NewClient(server.URL)
	_, err := client.GetIdentity(context.Background())
	if err == nil {
		t.Error("expected error for RPC error response")
	}
}

func TestGetIdentity_ConnectionError(t *testing.T) {
	client := NewClient("http://127.0.0.1:1") // nothing listening
	_, err := client.GetIdentity(context.Background())
	if err == nil {
		t.Error("expected error for connection failure")
	}
}
